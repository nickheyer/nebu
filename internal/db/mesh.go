package db

import (
	"context"
	"database/sql"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This node's identity in any mesh
type Identity struct {
	ID         string
	PublicKey  []byte
	PrivateKey []byte
	CreatedAt  time.Time
}

// Reads the identity, ErrNotFound before the first start made one
func (d *DB) GetIdentity(ctx context.Context) (*Identity, error) {
	items, err := list(ctx, d, `SELECT id, public_key, private_key, created_at FROM mesh_identity LIMIT 1`, func(rows *sql.Rows) (*Identity, error) {
		id := &Identity{}
		var created string
		if err := rows.Scan(&id.ID, &id.PublicKey, &id.PrivateKey, &created); err != nil {
			return nil, err
		}
		id.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		return id, nil
	})
	return one(items, err, "identity", "")
}

// Writes the identity once
func (d *DB) PutIdentity(ctx context.Context, id *Identity) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO mesh_identity (id, public_key, private_key, created_at) VALUES (?, ?, ?, ?)`, id.ID, id.PublicKey, id.PrivateKey, stamp(id.CreatedAt))
	return err
}

// The mesh this node belongs to
type MeshRow struct {
	ID, Name      string
	Secret        []byte
	TLS           bool
	CACertificate []byte
	CAKey         []byte
	Certificate   []byte
	CreatedAt     time.Time
	JoinedAt      time.Time
}

// Reads the mesh row, ErrNotFound when the node belongs to none
func (d *DB) GetMesh(ctx context.Context) (*MeshRow, error) {
	items, err := list(ctx, d, `SELECT id, name, secret, tls, ca_certificate, ca_key, certificate, created_at, joined_at FROM mesh LIMIT 1`, func(rows *sql.Rows) (*MeshRow, error) {
		m := &MeshRow{}
		var created, joined string
		if err := rows.Scan(&m.ID, &m.Name, &m.Secret, (*flag)(&m.TLS), &m.CACertificate, &m.CAKey, &m.Certificate, &created, &joined); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		m.JoinedAt, _ = time.Parse(time.RFC3339Nano, joined)
		return m, nil
	})
	return one(items, err, "mesh", "")
}

// Replaces the mesh row, a node belonging to at most one mesh
func (d *DB) PutMesh(ctx context.Context, m *MeshRow) error {
	return d.tx(ctx, func(exec execFn) error {
		if err := exec(`DELETE FROM mesh`); err != nil {
			return err
		}
		return exec(`INSERT INTO mesh (id, name, secret, tls, ca_certificate, ca_key, certificate, created_at, joined_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.Name, m.Secret, boolCol(m.TLS), blobCol(m.CACertificate), blobCol(m.CAKey), blobCol(m.Certificate), stamp(m.CreatedAt), stamp(m.JoinedAt))
	})
}

// A blob column takes an empty value where Go has nil
func blobCol(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// Forgets the mesh with its members, sessions, and links
func (d *DB) DeleteMesh(ctx context.Context) error {
	return d.tx(ctx, func(exec execFn) error {
		for _, table := range []string{"mesh", "mesh_members", "mesh_sessions", "mesh_links", "mesh_admissions"} {
			if err := exec(`DELETE FROM ` + table); err != nil {
				return err
			}
		}
		return nil
	})
}

// Writes a member's record as last synced
func (d *DB) PutMember(ctx context.Context, n *v1.Node) error {
	record, err := protojson.Marshal(n)
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO mesh_members (id, state, seen_at, record) VALUES (?, ?, ?, ?)`,
		n.GetId(), enumCol(n.GetState()), timeCol(n.GetSeenAt()), string(record))
	return err
}

// Lists every member's record
func (d *DB) ListMembers(ctx context.Context) ([]*v1.Node, error) {
	return list(ctx, d, `SELECT record, state, seen_at FROM mesh_members ORDER BY id`, func(rows *sql.Rows) (*v1.Node, error) {
		var record, state string
		var seen sql.NullString
		if err := rows.Scan(&record, &state, &seen); err != nil {
			return nil, err
		}
		n := &v1.Node{}
		if err := protojson.Unmarshal([]byte(record), n); err != nil {
			return nil, err
		}
		n.State = enumOf[v1.NodeState](state)
		if seen.Valid {
			if t, err := time.Parse(time.RFC3339Nano, seen.String); err == nil {
				n.SeenAt = timestamppb.New(t)
			}
		}
		return n, nil
	})
}

// Removes a member
func (d *DB) DeleteMember(ctx context.Context, id string) error {
	_, err := d.del(ctx, "mesh_members", "id", id)
	return err
}

// Session tokens with one peer
type SessionRow struct {
	PeerID string
	// The token the peer sends us and the token we send the peer
	Accept, Send string
	Expires      time.Time
	Granted      time.Time
}

func (d *DB) PutSession(ctx context.Context, s *SessionRow) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO mesh_sessions (peer_id, accept, send, expires_at, granted_at) VALUES (?, ?, ?, ?, ?)`, s.PeerID, s.Accept, s.Send, stamp(s.Expires), stamp(s.Granted))
	return err
}

func (d *DB) ListSessions(ctx context.Context) ([]*SessionRow, error) {
	return list(ctx, d, `SELECT peer_id, accept, send, expires_at, granted_at FROM mesh_sessions`, func(rows *sql.Rows) (*SessionRow, error) {
		s := &SessionRow{}
		var expires, granted string
		if err := rows.Scan(&s.PeerID, &s.Accept, &s.Send, &expires, &granted); err != nil {
			return nil, err
		}
		s.Expires, _ = time.Parse(time.RFC3339Nano, expires)
		s.Granted, _ = time.Parse(time.RFC3339Nano, granted)
		return s, nil
	})
}

func (d *DB) DeleteSession(ctx context.Context, peerID string) error {
	_, err := d.del(ctx, "mesh_sessions", "peer_id", peerID)
	return err
}

// Writes one measured link
func (d *DB) PutLink(ctx context.Context, l *v1.Link) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO mesh_links (from_id, to_id, rtt_us, rtt_p95_us, stream_bps, aggregate_bps, interface, interface_bps, rdma_device, class, measured_at, mtu, subnet, detail, bandwidth_held) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.GetFrom(), l.GetTo(), l.GetRttUs(), l.GetRttP95Us(), int64(l.GetStreamBytesPerSecond()), int64(l.GetAggregateBytesPerSecond()), l.GetInterface(), int64(l.GetInterfaceBitsPerSecond()), l.GetRdmaDevice(), enumCol(l.GetClass()), timeCol(l.GetMeasuredAt()), l.GetMtu(), boolCol(l.GetSubnet()), l.GetDetail(), boolCol(l.GetBandwidthHeld()))
	return err
}

// Lists the links measured from one node
func (d *DB) ListLinks(ctx context.Context, from string) ([]*v1.Link, error) {
	return list(ctx, d, `SELECT from_id, to_id, rtt_us, rtt_p95_us, stream_bps, aggregate_bps, interface, interface_bps, rdma_device, class, measured_at, mtu, subnet, detail, bandwidth_held FROM mesh_links WHERE from_id = ? ORDER BY from_id, to_id`, func(rows *sql.Rows) (*v1.Link, error) {
		l := &v1.Link{}
		var stream, aggregate, ifbps int64
		if err := rows.Scan(&l.From, &l.To, &l.RttUs, &l.RttP95Us, &stream, &aggregate, &l.Interface, &ifbps, &l.RdmaDevice, enumAt[v1.LinkClass]{&l.Class}, at{&l.MeasuredAt}, &l.Mtu, (*flag)(&l.Subnet), &l.Detail, (*flag)(&l.BandwidthHeld)); err != nil {
			return nil, err
		}
		l.StreamBytesPerSecond, l.AggregateBytesPerSecond, l.InterfaceBitsPerSecond = uint64(stream), uint64(aggregate), uint64(ifbps)
		return l, nil
	}, from)
}

// Drops the links to a member that left
func (d *DB) DeleteLinks(ctx context.Context, to string) error {
	_, err := d.del(ctx, "mesh_links", "to_id", to)
	return err
}

// Learned numbers of one device with the regression sums behind its fixed cost
type ThroughputRow struct {
	Throughput        *v1.Throughput
	AcceptanceSamples uint32
	SumX, SumY        float64
	SumXY, SumXX      float64
	Points            uint32
}

func (d *DB) PutThroughput(ctx context.Context, r *ThroughputRow) error {
	t := r.Throughput
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO throughput (device_id, stream_bps, compute_flops, fixed_seconds, acceptance, samples, acceptance_samples, sum_x, sum_y, sum_xy, sum_xx, points, updated_at, compute_samples) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.GetDeviceId(), t.GetStreamBytesPerSecond(), t.GetComputeFlops(), t.GetFixedSeconds(), t.GetAcceptance(), t.GetSamples(), r.AcceptanceSamples, r.SumX, r.SumY, r.SumXY, r.SumXX, r.Points, stamp(time.Now()), t.GetComputeSamples())
	return err
}

func (d *DB) ListThroughput(ctx context.Context) ([]*ThroughputRow, error) {
	return list(ctx, d, `SELECT device_id, stream_bps, compute_flops, fixed_seconds, acceptance, samples, acceptance_samples, sum_x, sum_y, sum_xy, sum_xx, points, updated_at, compute_samples FROM throughput ORDER BY device_id`, func(rows *sql.Rows) (*ThroughputRow, error) {
		r := &ThroughputRow{Throughput: &v1.Throughput{Source: "learned"}}
		t := r.Throughput
		if err := rows.Scan(&t.DeviceId, &t.StreamBytesPerSecond, &t.ComputeFlops, &t.FixedSeconds, &t.Acceptance, &t.Samples, &r.AcceptanceSamples, &r.SumX, &r.SumY, &r.SumXY, &r.SumXX, &r.Points, at{&t.UpdatedAt}, &t.ComputeSamples); err != nil {
			return nil, err
		}
		// Every decode sample is a point of the intercept fit, so the points are the stream samples.
		t.StreamSamples = r.Points
		return r, nil
	})
}

func (d *DB) PutRatio(ctx context.Context, r *v1.FormationRatio) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO formation_ratios (shape, runtime_id, link_class, ttft_ratio, tpt_ratio, samples, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		enumCol(r.GetShape()), r.GetRuntimeId(), enumCol(r.GetLinkClass()), r.GetTtftRatio(), r.GetTptRatio(), r.GetSamples(), stamp(time.Now()))
	return err
}

func (d *DB) ListRatios(ctx context.Context) ([]*v1.FormationRatio, error) {
	return list(ctx, d, `SELECT shape, runtime_id, link_class, ttft_ratio, tpt_ratio, samples, updated_at FROM formation_ratios ORDER BY shape, runtime_id, link_class`, func(rows *sql.Rows) (*v1.FormationRatio, error) {
		r := &v1.FormationRatio{}
		return r, rows.Scan(enumAt[v1.Shape]{&r.Shape}, &r.RuntimeId, enumAt[v1.LinkClass]{&r.LinkClass}, &r.TtftRatio, &r.TptRatio, &r.Samples, at{&r.UpdatedAt})
	})
}

func (d *DB) PutDeviceProfile(ctx context.Context, p *v1.DeviceProfile) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO device_profiles (pattern, stream_bps, compute_flops, fixed_seconds, updated_at) VALUES (?, ?, ?, ?, ?)`,
		p.GetPattern(), p.GetStreamBytesPerSecond(), p.GetComputeFlops(), p.GetFixedSeconds(), stamp(time.Now()))
	return err
}

func (d *DB) ListDeviceProfiles(ctx context.Context) ([]*v1.DeviceProfile, error) {
	return list(ctx, d, `SELECT pattern, stream_bps, compute_flops, fixed_seconds, updated_at FROM device_profiles ORDER BY pattern`, func(rows *sql.Rows) (*v1.DeviceProfile, error) {
		p := &v1.DeviceProfile{}
		return p, rows.Scan(&p.Pattern, &p.StreamBytesPerSecond, &p.ComputeFlops, &p.FixedSeconds, at{&p.UpdatedAt})
	})
}

func (d *DB) DeleteDeviceProfile(ctx context.Context, pattern string) (bool, error) {
	return d.del(ctx, "device_profiles", "pattern", pattern)
}

// Inserts or replaces a formation with its seats and candidates
func (d *DB) PutFormation(ctx context.Context, f *v1.Formation) error {
	request, err := protojson.Marshal(f.GetRequest())
	if err != nil {
		return err
	}
	// Seats and candidates live in their own tables.
	bare := proto.Clone(f.GetPlan()).(*v1.FormationPlan)
	if bare == nil {
		bare = &v1.FormationPlan{}
	}
	bare.Seats, bare.Candidates = nil, nil
	plan, err := protojson.Marshal(bare)
	if err != nil {
		return err
	}
	return d.tx(ctx, func(exec execFn) error {
		id := f.GetId()
		if err := exec(`INSERT OR REPLACE INTO formations (id, name, conductor, conductor_name, shape, state, error, task_id, slot_id, desired_running, runtime_id, endpoint, bytes_moved, sequence, source_id, repo, weight_group, request, plan, created_at, ready_at, stopped_at, updated_at, rendezvous, cache_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, f.GetName(), f.GetConductor(), f.GetConductorName(), enumCol(f.GetShape()), enumCol(f.GetState()), f.GetError(), f.GetTaskId(), f.GetSlotId(), boolCol(f.GetDesiredRunning()), f.GetRuntimeId(), f.GetEndpoint(), int64(f.GetBytesMoved()), int64(f.GetSequence()), f.GetSourceId(), f.GetRepo(), f.GetGroup(), string(request), string(plan), stamp(f.GetCreatedAt().AsTime()), timeCol(f.GetReadyAt()), timeCol(f.GetStoppedAt()), stamp(f.GetUpdatedAt().AsTime()), f.GetRendezvous(), f.GetCacheKey()); err != nil {
			return err
		}
		if err := clearChildren(exec, "formation_id", id, "formation_seats", "formation_candidates"); err != nil {
			return err
		}
		for i, s := range f.GetSeats() {
			placements, err := protojson.Marshal(&v1.FormationPlan{Seats: []*v1.Seat{{Placements: s.GetPlacements()}}})
			if err != nil {
				return err
			}
			memory := ""
			if s.GetMemory() != nil {
				raw, err := protojson.Marshal(s.GetMemory())
				if err != nil {
					return err
				}
				memory = string(raw)
			}
			// Triage and measurements travel in the same wrapper as placements.
			triage, err := protojson.Marshal(&v1.FormationPlan{Seats: []*v1.Seat{{Triage: s.GetTriage()}}})
			if err != nil {
				return err
			}
			measurements, err := protojson.Marshal(&v1.FormationPlan{Seats: []*v1.Seat{{Measurements: s.GetMeasurements()}}})
			if err != nil {
				return err
			}
			if err := exec(`INSERT INTO formation_seats (formation_id, position, node_id, node_name, role, rank, instance_id, state, error, endpoint, transport, layer_from, layer_to, read_bytes, cache_bytes, weight_bytes, install_id, exposed, phase, placements, memory, triage, measurements) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, i, s.GetNodeId(), s.GetNodeName(), s.GetRole(), s.GetRank(), s.GetInstanceId(), enumCol(s.GetState()), s.GetError(), s.GetEndpoint(), s.GetTransport(), s.GetLayerFrom(), s.GetLayerTo(), int64(s.GetReadBytes()), int64(s.GetCacheBytes()), int64(s.GetWeightBytes()), s.GetInstallId(), boolCol(s.GetExposed()), s.GetPhase(), string(placements), memory, string(triage), string(measurements)); err != nil {
				return err
			}
		}
		for i, c := range f.GetPlan().GetCandidates() {
			if err := exec(`INSERT INTO formation_candidates (formation_id, position, shape, node_ids, verdict, score, prefill_seconds, decode_seconds_per_token, reason, head, requests_per_second, tokens_per_second, speedup) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, i, enumCol(c.GetShape()), strings.Join(c.GetNodeIds(), ","), enumCol(c.GetVerdict()), c.GetScore(), c.GetPrefillSeconds(), c.GetDecodeSecondsPerToken(), c.GetReason(), c.GetHead(), c.GetRequestsPerSecond(), c.GetTokensPerSecond(), c.GetSpeedup()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Lists formations oldest first with seats and candidates
func (d *DB) ListFormations(ctx context.Context) ([]*v1.Formation, error) {
	return d.formations(ctx, ``)
}

// Returns one formation by id
func (d *DB) GetFormation(ctx context.Context, id string) (*v1.Formation, error) {
	items, err := d.formations(ctx, `WHERE id = ?`, id)
	return one(items, err, "formation", id)
}

func (d *DB) DeleteFormation(ctx context.Context, id string) (bool, error) {
	return d.del(ctx, "formations", "id", id)
}

func (d *DB) formations(ctx context.Context, where string, args ...any) ([]*v1.Formation, error) {
	out, err := list(ctx, d, `SELECT id, name, conductor, conductor_name, shape, state, error, task_id, slot_id, desired_running, runtime_id, endpoint, bytes_moved, sequence, source_id, repo, weight_group, request, plan, created_at, ready_at, stopped_at, updated_at, rendezvous, cache_key FROM formations `+where+` ORDER BY created_at, id`, func(rows *sql.Rows) (*v1.Formation, error) {
		f := &v1.Formation{}
		var request, plan string
		var moved, sequence int64
		if err := rows.Scan(&f.Id, &f.Name, &f.Conductor, &f.ConductorName, enumAt[v1.Shape]{&f.Shape}, enumAt[v1.FormationState]{&f.State}, &f.Error, &f.TaskId, &f.SlotId, (*flag)(&f.DesiredRunning), &f.RuntimeId, &f.Endpoint, &moved, &sequence, &f.SourceId, &f.Repo, &f.Group, &request, &plan, at{&f.CreatedAt}, at{&f.ReadyAt}, at{&f.StoppedAt}, at{&f.UpdatedAt}, &f.Rendezvous, &f.CacheKey); err != nil {
			return nil, err
		}
		f.BytesMoved, f.Sequence = uint64(moved), uint64(sequence)
		f.Request = &v1.RunRequest{}
		if err := protojson.Unmarshal([]byte(request), f.Request); err != nil {
			return nil, err
		}
		f.Plan = &v1.FormationPlan{}
		if err := protojson.Unmarshal([]byte(plan), f.Plan); err != nil {
			return nil, err
		}
		return f, nil
	}, args...)
	if err != nil {
		return nil, err
	}
	for _, f := range out {
		if f.Seats, err = list(ctx, d, `SELECT node_id, node_name, role, rank, instance_id, state, error, endpoint, transport, layer_from, layer_to, read_bytes, cache_bytes, weight_bytes, install_id, exposed, phase, placements, memory, triage, measurements FROM formation_seats WHERE formation_id = ? ORDER BY position`, func(rows *sql.Rows) (*v1.Seat, error) {
			s := &v1.Seat{}
			var read, cache, weight int64
			var placements, memory, triage, measurements string
			if err := rows.Scan(&s.NodeId, &s.NodeName, &s.Role, &s.Rank, &s.InstanceId, enumAt[v1.InstanceState]{&s.State}, &s.Error, &s.Endpoint, &s.Transport, &s.LayerFrom, &s.LayerTo, &read, &cache, &weight, &s.InstallId, (*flag)(&s.Exposed), &s.Phase, &placements, &memory, &triage, &measurements); err != nil {
				return nil, err
			}
			s.ReadBytes, s.CacheBytes, s.WeightBytes = uint64(read), uint64(cache), uint64(weight)
			wrapped := &v1.FormationPlan{}
			if err := protojson.Unmarshal([]byte(placements), wrapped); err != nil {
				return nil, err
			}
			if len(wrapped.GetSeats()) > 0 {
				s.Placements = wrapped.GetSeats()[0].GetPlacements()
			}
			for column, take := range map[string]func(*v1.Seat){triage: func(in *v1.Seat) { s.Triage = in.GetTriage() }, measurements: func(in *v1.Seat) { s.Measurements = in.GetMeasurements() }} {
				wrapped := &v1.FormationPlan{}
				if err := protojson.Unmarshal([]byte(column), wrapped); err != nil {
					return nil, err
				}
				if len(wrapped.GetSeats()) > 0 {
					take(wrapped.GetSeats()[0])
				}
			}
			if memory != "" {
				s.Memory = &v1.MemoryPlan{}
				if err := protojson.Unmarshal([]byte(memory), s.Memory); err != nil {
					return nil, err
				}
			}
			return s, nil
		}, f.GetId()); err != nil {
			return nil, err
		}
		f.Plan.Seats = f.Seats
		if f.Plan.Candidates, err = list(ctx, d, `SELECT shape, node_ids, verdict, score, prefill_seconds, decode_seconds_per_token, reason, head, requests_per_second, tokens_per_second, speedup FROM formation_candidates WHERE formation_id = ? ORDER BY position`, func(rows *sql.Rows) (*v1.Candidate, error) {
			c := &v1.Candidate{}
			var ids string
			if err := rows.Scan(enumAt[v1.Shape]{&c.Shape}, &ids, enumAt[v1.FitVerdict]{&c.Verdict}, &c.Score, &c.PrefillSeconds, &c.DecodeSecondsPerToken, &c.Reason, &c.Head, &c.RequestsPerSecond, &c.TokensPerSecond, &c.Speedup); err != nil {
				return nil, err
			}
			if ids != "" {
				c.NodeIds = strings.Split(ids, ",")
			}
			return c, nil
		}, f.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Writes an admission, replacing the earlier copy
func (d *DB) PutAdmission(ctx context.Context, a *v1.Admission) error {
	record, err := protojson.Marshal(a)
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO mesh_admissions (id, side, state, node_id, mesh_hash, record, created_at, updated_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.GetId(), enumCol(a.GetSide()), enumCol(a.GetState()), a.GetNode().GetId(), a.GetMeshHash(), string(record), stamp(a.GetCreatedAt().AsTime()), stamp(a.GetUpdatedAt().AsTime()), timeCol(a.GetExpiresAt()))
	return err
}

// Lists every admission, oldest first
func (d *DB) ListAdmissions(ctx context.Context) ([]*v1.Admission, error) {
	return list(ctx, d, `SELECT record FROM mesh_admissions ORDER BY created_at, id`, func(rows *sql.Rows) (*v1.Admission, error) {
		var record string
		if err := rows.Scan(&record); err != nil {
			return nil, err
		}
		a := &v1.Admission{}
		if err := protojson.Unmarshal([]byte(record), a); err != nil {
			return nil, err
		}
		return a, nil
	})
}

// Removes one admission
func (d *DB) DeleteAdmission(ctx context.Context, id string) error {
	_, err := d.del(ctx, "mesh_admissions", "id", id)
	return err
}

// Removes every admission
func (d *DB) DeleteAdmissions(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM mesh_admissions`)
	return err
}

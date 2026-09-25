package mesh

import (
	"context"
	"sort"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/pull"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// A member holding a model, with the connection to fetch its blobs
type Holder struct {
	Node   *v1.Node
	Client *Client
	Stored *v1.StoredSummary
}

// How far a link's class puts a member, nearer first
func classRank(c v1.LinkClass) int {
	switch c {
	case v1.LinkClass_LINK_CLASS_FABRIC:
		return 0
	case v1.LinkClass_LINK_CLASS_FAST:
		return 1
	case v1.LinkClass_LINK_CLASS_LAN:
		return 2
	case v1.LinkClass_LINK_CLASS_SLOW:
		return 3
	}
	return 4
}

// Members holding a model, best link first: measured links by class then round trip, members
// whose link is not measured yet after them, ties by name. An empty group takes any group of the
// repo, an empty source any source.
func (m *Manager) Holders(ctx context.Context, sourceID, repo, group string) []Holder {
	type pick struct {
		rec      *v1.Node
		stored   *v1.StoredSummary
		measured bool
		rank     int
		rtt      uint32
	}
	var picks []pick
	m.mu.Lock()
	for id, mb := range m.members {
		if mb.rec.GetState() != v1.NodeState_NODE_STATE_READY || mb.rec.GetAddress() == "" {
			continue
		}
		for _, s := range mb.rec.GetStored() {
			if s.GetRepo() != repo || group != "" && s.GetGroup() != group || sourceID != "" && s.GetSourceId() != sourceID {
				continue
			}
			p := pick{rec: mb.rec, stored: s}
			if l, ok := m.links[id]; ok && l.GetMeasuredAt() != nil {
				p.measured, p.rank, p.rtt = true, classRank(l.GetClass()), l.GetRttUs()
			}
			picks = append(picks, p)
			break
		}
	}
	m.mu.Unlock()
	sort.Slice(picks, func(i, j int) bool {
		a, b := picks[i], picks[j]
		if a.measured != b.measured {
			return a.measured
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.rtt != b.rtt {
			return a.rtt < b.rtt
		}
		if a.rec.GetName() != b.rec.GetName() {
			return a.rec.GetName() < b.rec.GetName()
		}
		return a.rec.GetId() < b.rec.GetId()
	})
	var out []Holder
	for _, p := range picks {
		cl, err := m.Client(ctx, p.rec.GetId())
		if err != nil {
			m.Log.Warn("member holding a model is unreachable", "node", p.rec.GetName(), "repo", repo, "err", err)
			continue
		}
		out = append(out, Holder{Node: p.rec, Client: cl, Stored: p.stored})
	}
	return out
}

// The puller's view of the mesh: members holding a model, best link first, with the connections
// their blobs are read over, and the manifest each keeps for it
type Source struct {
	Mesh *Manager
}

func (s Source) Holders(ctx context.Context, sourceID, repo, group string) []pull.Holder {
	var out []pull.Holder
	for _, h := range s.Mesh.Holders(ctx, sourceID, repo, group) {
		out = append(out, pull.Holder{NodeID: h.Node.GetId(), Name: h.Node.GetName(), Base: h.Client.Base, HTTP: h.Client.HTTP, Stored: h.Node.GetStored()})
	}
	return out
}

func (s Source) Manifest(ctx context.Context, h pull.Holder, sourceID, repo, group string) (*v1.StoredModel, error) {
	cl, err := s.Mesh.Client(ctx, h.NodeID)
	if err != nil {
		return nil, err
	}
	resp, err := cl.Mesh.GetStored(ctx, connect.NewRequest(&v1.GetStoredRequest{SourceId: sourceID, Repo: repo, Group: group}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetModel(), nil
}

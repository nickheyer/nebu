package formations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/tasks"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

const kindPull = "pull"

// The directory a formation's relay seats save slots in on this node: <cache dir>/slots/<formation>
func (m *Manager) slotDir(formationID string) string {
	return filepath.Join(m.CacheDir, "slots", formationID)
}

// Fetches a relay's saved slot from the prefill seat's node into this node's slot directory, the
// cache moving over the mesh blob channel, and returns where it landed
func (m *Manager) MoveSlot(ctx context.Context, formationID, fromNode, file string) (string, uint64, error) {
	if formationID == "" || file == "" || strings.ContainsAny(formationID+file, "/\\") || strings.HasPrefix(file, ".") {
		return "", 0, fmt.Errorf("%w: a slot move names a formation and a file", ErrFormation)
	}
	dir := m.slotDir(formationID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	dest := filepath.Join(dir, file)
	if fromNode == m.self() {
		info, err := os.Stat(dest)
		if err != nil {
			return "", 0, fmt.Errorf("slot file %s: %w", file, err)
		}
		return dest, uint64(info.Size()), nil
	}
	cl, err := m.Mesh.Client(ctx, fromNode)
	if err != nil {
		return "", 0, err
	}
	q := url.Values{"formation": {formationID}, "file": {file}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cl.Base+"/mesh/slot?"+q.Encode(), nil)
	if err != nil {
		return "", 0, err
	}
	start := time.Now()
	resp, err := cl.HTTP.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("slot from %s: %w", m.nodeName(fromNode), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, fmt.Errorf("slot from %s: %s: %s", m.nodeName(fromNode), resp.Status, strings.TrimSpace(string(raw)))
	}
	want := resp.Header.Get(SlotDigestHeader)
	if !strings.HasPrefix(want, "sha256:") {
		return "", 0, fmt.Errorf("slot from %s: the file came without its sha256 digest", m.nodeName(fromNode))
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	if got := "sha256:" + hex.EncodeToString(sum.Sum(nil)); got != want {
		os.Remove(tmp)
		return "", 0, fmt.Errorf("slot from %s: %s arrived as %s, not %s", m.nodeName(fromNode), file, got, want)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", 0, err
	}
	if m.Perf != nil {
		if err := m.Perf.RecordDisk(ctx, uint64(n), time.Since(start).Seconds()); err != nil {
			m.Log.Warn("disk throughput write failed", "err", err)
		}
	}
	return dest, uint64(n), nil
}

// The header a served slot file carries its sha256 digest in, verified where it lands
const SlotDigestHeader = "X-Nebu-Digest"

// Removes a relay's slot file from this node once the decode seat restored it or the relay failed
func (m *Manager) DropSlot(formationID, file string) error {
	if formationID == "" || file == "" || strings.ContainsAny(formationID+file, "/\\") || strings.HasPrefix(file, ".") {
		return fmt.Errorf("%w: a slot drop names a formation and a file", ErrFormation)
	}
	err := os.Remove(filepath.Join(m.slotDir(formationID), file))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Removes a relay's slot file from a node, for the gateway's relay
func (m *Manager) DropSlotOn(ctx context.Context, nodeID, formationID, file string) error {
	if nodeID == m.self() {
		return m.DropSlot(formationID, file)
	}
	cl, err := m.Mesh.Client(ctx, nodeID)
	if err != nil {
		return err
	}
	_, err = cl.Mesh.DropSlot(ctx, connect.NewRequest(&v1.DropSlotRequest{FormationId: formationID, File: file}))
	return err
}

// Moves a slot file to a node from another, for the gateway's relay
func (m *Manager) MoveSlotTo(ctx context.Context, formationID, toNode, fromNode, file string) error {
	if toNode == m.self() {
		_, _, err := m.MoveSlot(ctx, formationID, fromNode, file)
		return err
	}
	cl, err := m.Mesh.Client(ctx, toNode)
	if err != nil {
		return err
	}
	_, err = cl.Mesh.MoveSlot(ctx, connect.NewRequest(&v1.MoveSlotRequest{FormationId: formationID, FromNodeId: fromNode, File: file}))
	return err
}

// A connection to a member's gateway, for requests forwarded there
func (m *Manager) Dial(ctx context.Context, nodeID string) (string, *http.Client, error) {
	cl, err := m.Mesh.Client(ctx, nodeID)
	if err != nil {
		return "", nil, err
	}
	return cl.Base, cl.HTTP, nil
}

// How far two nodes are apart by the class of the link between them as either measured it: the
// same node first, then fabric, fast, lan, slow, and unmeasured last
func (m *Manager) LinkBetween(from, to string) int {
	if from == to {
		return 0
	}
	rank := 10
	for _, l := range m.Mesh.Links() {
		if l.GetFrom() == from && l.GetTo() == to || l.GetFrom() == to && l.GetTo() == from {
			rank = min(rank, classRank(l.GetClass()))
		}
	}
	return rank
}

// How near a link's class puts two nodes
func classRank(c v1.LinkClass) int {
	switch c {
	case v1.LinkClass_LINK_CLASS_FABRIC:
		return 1
	case v1.LinkClass_LINK_CLASS_FAST:
		return 2
	case v1.LinkClass_LINK_CLASS_LAN:
		return 3
	case v1.LinkClass_LINK_CLASS_SLOW:
		return 4
	}
	return 9
}

// Pulls a model onto a member, following the member's pull in one task here whose progress
// mirrors the member's byte progress
func (m *Manager) PullTo(ctx context.Context, nodeID string, req *v1.PullRequest) (*v1.Task, error) {
	rec, err := m.Mesh.Node(nodeID)
	if err != nil {
		return nil, err
	}
	if rec.GetId() == m.self() {
		req = &v1.PullRequest{SourceId: req.GetSourceId(), Repo: req.GetRepo(), Revision: req.GetRevision(), Group: req.GetGroup(), Alone: req.GetAlone()}
		return m.Puller.Pull(ctx, req)
	}
	if rec.GetState() != v1.NodeState_NODE_STATE_READY {
		return nil, fmt.Errorf("%w: %s is %s", ErrFormation, rec.GetName(), stateWord(rec.GetState()))
	}
	labels := map[string]string{"source": req.GetSourceId(), "repo": req.GetRepo(), "group": req.GetGroup(), "node": rec.GetId()}
	title := fmt.Sprintf("pull %s %s onto %s", req.GetRepo(), req.GetGroup(), rec.GetName())
	return m.Tasks.Start(kindPull, title, labels, func(ctx context.Context, h *tasks.Handle) error {
		return m.ensure(ctx, h, h.Progress, rec.GetId(), req.GetSourceId(), req.GetRepo(), req.GetGroup(), req.GetRevision())
	}), nil
}

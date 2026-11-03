package mesh

import (
	"crypto/x509"
	"fmt"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Condition types the gossip rounds set on every node record
const (
	ConditionReachable        = "Reachable"
	ConditionSynced           = "Synced"
	ConditionCertificateValid = "CertificateValid"
)

// Upserts a condition by type, moving its transition time only when the status changes, true when anything changed
func setCondition(list *[]*v1.Condition, typ string, ok bool, reason, message string) bool {
	status := v1.ConditionStatus_CONDITION_STATUS_FALSE
	if ok {
		status = v1.ConditionStatus_CONDITION_STATUS_TRUE
	}
	for _, c := range *list {
		if c.GetType() != typ {
			continue
		}
		changed := c.GetStatus() != status || c.GetReason() != reason || c.GetMessage() != message
		if c.GetStatus() != status {
			c.LastTransitionTime = timestamppb.Now()
		}
		c.Status, c.Reason, c.Message = status, reason, message
		return changed
	}
	*list = append(*list, &v1.Condition{Type: typ, Status: status, Reason: reason, Message: message, LastTransitionTime: timestamppb.Now()})
	return true
}

// Sets a member's Reachable, Synced and CertificateValid conditions from this node's view, true when any changed
func (m *Manager) memberConditionsLocked(mb *member, now time.Time) bool {
	conds := mb.rec.Conditions
	changed := false
	switch mb.rec.GetState() {
	case v1.NodeState_NODE_STATE_READY:
		changed = setCondition(&conds, ConditionReachable, true, "Seen", fmt.Sprintf("heard within the last %d rounds in some peer's view", UnreachableAfter)) || changed
	case v1.NodeState_NODE_STATE_UNREACHABLE:
		changed = setCondition(&conds, ConditionReachable, false, "Unseen", fmt.Sprintf("not heard for %d rounds in any peer's view", UnreachableAfter)) || changed
	default:
		changed = setCondition(&conds, ConditionReachable, false, "Gone", "not heard for "+GoneAfter.String()) || changed
	}
	switch {
	case mb.sketch:
		changed = setCondition(&conds, ConditionSynced, false, "NeverSynced", "known from gossip alone, no sync has carried its record yet") || changed
	case mb.advertised > mb.held():
		changed = setCondition(&conds, ConditionSynced, false, "Behind", "gossip names a newer record than the one held") || changed
	default:
		changed = setCondition(&conds, ConditionSynced, true, "Current", "the newest record gossip names is held") || changed
	}
	ok, reason, message := m.certificateStateLocked(mb.rec.GetCertificate(), now)
	changed = setCondition(&conds, ConditionCertificateValid, ok, reason, message) || changed
	mb.rec.Conditions = conds
	return changed
}

// Whether a certificate in DER is valid under the current authority, with the reason and message saying why
func (m *Manager) certificateStateLocked(der []byte, now time.Time) (bool, string, string) {
	if len(der) == 0 {
		return false, "Missing", "no certificate is on record"
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return false, "Unreadable", err.Error()
	}
	switch {
	case m.creds == nil || !m.creds.issued(cert):
		return false, "UnknownAuthority", "the certificate is not signed by the current mesh authority"
	case m.revoked[serialOf(cert)] != nil:
		return false, "Revoked", "certificate " + serialOf(cert) + " is revoked"
	case now.After(cert.NotAfter):
		return false, "Expired", "the certificate expired at " + cert.NotAfter.Format(time.RFC3339)
	case cert.NotAfter.Sub(now) < RenewWithin:
		return true, "RenewalDue", "the certificate expires at " + cert.NotAfter.Format(time.RFC3339) + " and is renewed by the issuer"
	}
	return true, "Valid", "the certificate expires at " + cert.NotAfter.Format(time.RFC3339)
}

// This node's own conditions, reachable as itself, synced once every ready member holds its record, and its certificate
func (m *Manager) selfConditionsLocked() []*v1.Condition {
	var conds []*v1.Condition
	setCondition(&conds, ConditionReachable, true, "Self", "this node")
	behind := 0
	for _, mb := range m.members {
		if mb.rec.GetState() == v1.NodeState_NODE_STATE_READY && mb.sent < m.seq {
			behind++
		}
	}
	if behind > 0 {
		setCondition(&conds, ConditionSynced, false, "Pending", fmt.Sprintf("%d ready members hold an older record", behind))
	} else {
		setCondition(&conds, ConditionSynced, true, "Acknowledged", "every ready member holds the current record")
	}
	var der []byte
	if m.creds != nil {
		der = m.creds.leaf.Raw
	}
	ok, reason, message := m.certificateStateLocked(der, time.Now())
	setCondition(&conds, ConditionCertificateValid, ok, reason, message)
	return conds
}

package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

func meshCommands() command {
	return command{name: "mesh", summary: "nodes on one network pooling their devices", run: runMeshStatus, sub: []command{
		{name: "init", summary: "make a mesh on this node", run: runMeshInit},
		{name: "token", summary: "print a join token", run: runMeshToken},
		{name: "join", summary: "join the mesh a token names", run: runMeshJoin},
		{name: "leave", summary: "leave the mesh", run: runMeshLeave},
		{name: "status", summary: "nodes, links, and formations", run: runMeshStatus},
		{name: "nearby", summary: "nodes and meshes heard on the network, and admissions under way", run: runMeshNearby},
		{name: "invite", summary: "invite a node heard on the network, or one at an address", run: runMeshInvite},
		{name: "ask", summary: "ask to join a mesh heard on the network, or accept its invitation", run: runMeshAsk},
		{name: "admit", summary: "admit a node that asked to join", run: runMeshAdmit},
		{name: "refuse", summary: "deny a node that asked, or decline an invitation", run: runMeshRefuse},
		{name: "dismiss", summary: "clear a settled admission", run: runMeshDismiss},
		{name: "nodes", summary: "list members", run: runMeshNodes},
		{name: "links", summary: "measured links, measuring them again with --probe", run: runMeshLinks},
		{name: "rotate", summary: "issue a new secret to every reachable member", run: runMeshRotate},
		{name: "profile", summary: "declared device numbers the planner starts from", run: runMeshProfiles, sub: []command{
			{name: "list", summary: "list device profiles", run: runMeshProfiles},
			{name: "set", summary: "set numbers for devices matching a pattern", run: runMeshProfileSet},
			{name: "delete", summary: "remove numbers set for a pattern", run: runMeshProfileDelete},
		}},
	}}
}

func formationCommands() command {
	return command{name: "formations", summary: "models running across nodes", run: runFormationsList, sub: []command{
		{name: "list", summary: "list formations", run: runFormationsList},
		{name: "show", summary: "show a formation with its seats and the shapes rejected", run: runFormationsShow},
		{name: "stop", summary: "stop a formation", run: runFormationsStop},
		{name: "logs", summary: "show or follow a seat's output", run: runFormationsLogs},
	}}
}

func runMeshInit(ctx context.Context, e *env, args []string) error {
	fs := e.flags("mesh init")
	name := fs.String("name", "", "mesh name")
	useTLS := fs.Bool("tls", false, "make a certificate authority so members verify each other")
	if _, err := e.parse(fs, args, 0, 0, "mesh init [--name NAME] [--tls]"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.Init(ctx, connect.NewRequest(&v1.InitRequest{Name: *name, Tls: *useTLS}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		m := resp.Msg.GetMesh()
		fmt.Fprintf(w, "mesh %s %s at %s, tls %s\n", m.GetName(), m.GetId(), m.GetAddress(), yes(m.GetTls()))
		fmt.Fprintf(w, "other nodes ask to join from their Mesh page or with nebu mesh ask; on a network that passes no beacons, this join token joins one:\n%s\n", resp.Msg.GetToken())
	})
}

func runMeshToken(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("mesh token"), args, 0, 0, "mesh token"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.Token(ctx, connect.NewRequest(&v1.TokenRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintln(w, resp.Msg.GetToken()) })
}

func runMeshJoin(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("mesh join"), args, 1, 1, "mesh join <token>")
	if err != nil {
		return err
	}
	resp, err := e.cl.mesh.Join(ctx, connect.NewRequest(&v1.JoinRequest{Token: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		m, b := resp.Msg.GetMesh(), resp.Msg.GetBootstrap()
		fmt.Fprintf(w, "joined %s %s through %s at %s, %d members\n", m.GetName(), m.GetId(), b.GetName(), b.GetAddress(), m.GetMembers())
		for _, warn := range resp.Msg.GetWarnings() {
			fmt.Fprintln(w, "warning:", warn)
		}
	})
}

func runMeshLeave(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("mesh leave"), args, 0, 0, "mesh leave"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.Leave(ctx, connect.NewRequest(&v1.LeaveRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "left %s %s\n", resp.Msg.GetMesh().GetName(), resp.Msg.GetMesh().GetId())
	})
}

func runMeshRotate(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("mesh rotate"), args, 0, 0, "mesh rotate"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.Rotate(ctx, connect.NewRequest(&v1.RotateRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "rotated the secret of %s: %d members took it", resp.Msg.GetMesh().GetName(), len(resp.Msg.GetRotated()))
		if len(resp.Msg.GetMissed()) > 0 {
			fmt.Fprintf(w, ", %d must join again: %s", len(resp.Msg.GetMissed()), strings.Join(resp.Msg.GetMissed(), ", "))
		}
		fmt.Fprintln(w)
	})
}

func runMeshNearby(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("mesh nearby"), args, 0, 0, "mesh nearby"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.Discover(ctx, connect.NewRequest(&v1.DiscoverRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderStatus(w, resp.Msg.GetStatus()) })
}

func renderStatus(w io.Writer, st *v1.MeshStatus) {
	switch {
	case st.GetMesh() != nil:
		fmt.Fprintf(w, "this node is %s %s, a member of %s, reached at %s\n", st.GetNodeName(), st.GetNodeId()[:min(12, len(st.GetNodeId()))], st.GetMesh().GetName(), orDash(st.GetAddress()))
	default:
		fmt.Fprintf(w, "this node is %s %s, in no mesh, reached at %s\n", st.GetNodeName(), st.GetNodeId()[:min(12, len(st.GetNodeId()))], orDash(st.GetAddress()))
	}
	if st.GetListenError() != "" {
		fmt.Fprintf(w, "node traffic listener not bound: %s\n", st.GetListenError())
	}
	if !st.GetAnnounce() {
		fmt.Fprintln(w, "beacons are off (mesh.announce), so nothing is heard; invite and ask by address")
	}
	section(w, "meshes heard")
	var rows [][]string
	for _, nm := range st.GetMeshes() {
		var names []string
		for _, n := range nm.GetNodes() {
			names = append(names, n.GetName())
		}
		rows = append(rows, []string{nm.GetName(), nm.GetHash()[:min(12, len(nm.GetHash()))], strconv.Itoa(int(nm.GetMembers())), yes(nm.GetTls()), strings.Join(names, ", "), when(nm.GetHeardAt(), time.Kitchen)})
	}
	table(w, []string{"MESH", "HASH", "MEMBERS", "TLS", "HEARD FROM", "HEARD"}, rows)
	section(w, "nodes heard")
	rows = nil
	for _, n := range st.GetNodes() {
		in := "no mesh"
		switch {
		case n.GetMember():
			in = "this mesh"
		case n.GetMeshName() != "":
			in = n.GetMeshName()
		}
		rows = append(rows, []string{n.GetName(), n.GetId()[:min(12, len(n.GetId()))], orDash(n.GetAddress()), in, n.GetOs() + "/" + n.GetArch(), orDash(n.GetVersion()), when(n.GetHeardAt(), time.Kitchen)})
	}
	table(w, []string{"NAME", "ID", "ADDRESS", "MESH", "OS", "VERSION", "HEARD"}, rows)
	section(w, "admissions")
	admissionsTable(w, st.GetAdmissions())
}

func admissionsTable(w io.Writer, list []*v1.Admission) {
	var rows [][]string
	for _, a := range list {
		what := "asked to join"
		switch {
		case a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_MEMBER && a.GetInvited() && !a.GetAsked():
			what = "invited"
		case a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_MEMBER && a.GetInvited():
			what = "invited, accepted"
		case a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE && a.GetInvited() && !a.GetAsked():
			what = "invites this node"
		case a.GetSide() == v1.AdmissionSide_ADMISSION_SIDE_CANDIDATE:
			what = "this node asked"
		}
		rows = append(rows, []string{a.GetId(), describeNode(a.GetNode()), a.GetMeshName(), what, loud(a.GetState()), orDash(a.GetBy()), truncate(a.GetDetail(), 60)})
	}
	table(w, []string{"ID", "NODE", "MESH", "WHAT", "STATE", "BY", "DETAIL"}, rows)
}

func describeNode(n *v1.NearbyNode) string {
	if n.GetName() != "" && n.GetAddress() != "" {
		return n.GetName() + " " + n.GetAddress()
	}
	if n.GetName() != "" {
		return n.GetName()
	}
	return orDash(n.GetAddress())
}

func runMeshInvite(ctx context.Context, e *env, args []string) error {
	fs := e.flags("mesh invite")
	address := fs.String("address", "", "the node's address, for a network that passes no beacons")
	positional, err := e.parse(fs, args, 0, 1, "mesh invite <node name|id> | mesh invite --address host:port")
	if err != nil {
		return err
	}
	node := ""
	if len(positional) > 0 {
		node = positional[0]
	}
	if node == "" && *address == "" {
		return fmt.Errorf("usage: nebu mesh invite <node name|id> | nebu mesh invite --address host:port")
	}
	resp, err := e.cl.mesh.Invite(ctx, connect.NewRequest(&v1.InviteRequest{NodeId: node, Address: *address}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		a := resp.Msg.GetAdmission()
		fmt.Fprintf(w, "invited %s to %s; its Mesh page decides, and it joins on accepting\n", describeNode(a.GetNode()), a.GetMeshName())
	})
}

func runMeshAsk(ctx context.Context, e *env, args []string) error {
	fs := e.flags("mesh ask")
	address := fs.String("address", "", "a member's address, for a network that passes no beacons")
	via := fs.String("via", "", "the member to ask, by name or id, else the one heard most recently")
	positional, err := e.parse(fs, args, 0, 1, "mesh ask <mesh name|hash> [--via node] | mesh ask --address host:port")
	if err != nil {
		return err
	}
	hash := ""
	if len(positional) > 0 {
		hash = positional[0]
		nearby, err := e.cl.mesh.Discover(ctx, connect.NewRequest(&v1.DiscoverRequest{}))
		if err != nil {
			return err
		}
		for _, nm := range nearby.Msg.GetStatus().GetMeshes() {
			if nm.GetName() == hash {
				hash = nm.GetHash()
				break
			}
		}
	}
	if hash == "" && *via == "" && *address == "" {
		return fmt.Errorf("usage: nebu mesh ask <mesh name|hash> [--via node] | nebu mesh ask --address host:port")
	}
	resp, err := e.cl.mesh.Ask(ctx, connect.NewRequest(&v1.AskRequest{MeshHash: hash, NodeId: *via, Address: *address}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		a := resp.Msg.GetAdmission()
		fmt.Fprintf(w, "asked %s to join %s: %s; any member's Mesh page admits, and this node joins on its own\n", describeNode(a.GetNode()), a.GetMeshName(), text.Enum(a.GetState()))
	})
}

func runMeshAdmit(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("mesh admit"), args, 1, 1, "mesh admit <node name|id|admission id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.mesh.Admit(ctx, connect.NewRequest(&v1.AdmitRequest{AdmissionId: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		a := resp.Msg.GetAdmission()
		fmt.Fprintf(w, "%s %s\n", describeNode(a.GetNode()), text.Enum(a.GetState()))
	})
}

func runMeshRefuse(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("mesh refuse"), args, 1, 1, "mesh refuse <node name|id|mesh name|admission id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.mesh.Refuse(ctx, connect.NewRequest(&v1.RefuseRequest{AdmissionId: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		a := resp.Msg.GetAdmission()
		fmt.Fprintf(w, "%s %s: %s\n", describeNode(a.GetNode()), text.Enum(a.GetState()), a.GetDetail())
	})
}

func runMeshDismiss(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("mesh dismiss"), args, 1, 1, "mesh dismiss <node name|id|mesh name|admission id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.mesh.Dismiss(ctx, connect.NewRequest(&v1.DismissRequest{AdmissionId: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintln(w, "dismissed") })
}

func runMeshStatus(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("mesh status"), args, 0, 0, "mesh status"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.GetMesh(ctx, connect.NewRequest(&v1.GetMeshRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		m := resp.Msg.GetMesh()
		if m == nil {
			fmt.Fprintln(w, "this node belongs to no mesh. nebu mesh nearby lists the meshes heard on the network, nebu mesh ask joins one, nebu mesh init makes one")
			return
		}
		fmt.Fprintf(w, "mesh %s %s: %d members, tls %s, announce %s, exposure %s, this node at %s\n", m.GetName(), m.GetId(), m.GetMembers(), yes(m.GetTls()), yes(m.GetAnnounce()), m.GetExposure(), orDash(m.GetAddress()))
		section(w, "nodes")
		nodesTable(w, resp.Msg.GetNodes())
		section(w, "links")
		linksTable(w, allLinks(resp.Msg.GetNodes()), resp.Msg.GetNodes())
		section(w, "formations")
		formationsTable(w, resp.Msg.GetFormations())
	})
}

func runMeshNodes(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("mesh nodes"), args, 0, 0, "mesh nodes"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { nodesTable(w, resp.Msg.GetNodes()) })
}

func nodesTable(w io.Writer, nodes []*v1.Node) {
	var rows [][]string
	for _, n := range nodes {
		var devices []string
		for _, d := range n.GetProfile().GetDevices() {
			if d.GetKind() != v1.DeviceKind_DEVICE_KIND_CPU {
				devices = append(devices, d.GetName())
			}
		}
		var installs []string
		for _, in := range n.GetInstalls() {
			installs = append(installs, in.GetRuntimeId())
		}
		sort.Strings(installs)
		self := ""
		if n.GetSelf() {
			self = " (this node)"
		}
		state := loud(n.GetState())
		if n.GetSelf() {
			state = "READY"
		}
		rows = append(rows, []string{n.GetName() + self, n.GetId()[:min(12, len(n.GetId()))], state, orDash(n.GetAddress()), n.GetOs() + "/" + n.GetArch(), truncate(orDash(strings.Join(devices, ", ")), 40), strconv.Itoa(len(n.GetStored())), orDash(strings.Join(installs, ",")), when(n.GetSeenAt(), time.Kitchen)})
	}
	table(w, []string{"NAME", "ID", "STATE", "ADDRESS", "OS", "DEVICES", "MODELS", "RUNTIMES", "SEEN"}, rows)
}

func allLinks(nodes []*v1.Node) []*v1.Link {
	var out []*v1.Link
	for _, n := range nodes {
		out = append(out, n.GetLinks()...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetFrom() != out[j].GetFrom() {
			return out[i].GetFrom() < out[j].GetFrom()
		}
		return out[i].GetTo() < out[j].GetTo()
	})
	return out
}

func nodeName(nodes []*v1.Node, id string) string {
	for _, n := range nodes {
		if n.GetId() == id && n.GetName() != "" {
			return n.GetName()
		}
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func linksTable(w io.Writer, links []*v1.Link, nodes []*v1.Node) {
	var rows [][]string
	for _, l := range links {
		detail := l.GetDetail()
		if l.GetBandwidthHeld() {
			detail = "bandwidth held; " + detail
		}
		iface := orDash(l.GetInterface())
		if l.GetInterfaceBitsPerSecond() > 0 {
			iface += fmt.Sprintf(" %s", gbitsText(l.GetInterfaceBitsPerSecond()/8))
		}
		if l.GetMtu() > 0 {
			iface += fmt.Sprintf(" mtu %d", l.GetMtu())
		}
		if !l.GetSubnet() && l.GetInterface() != "" {
			iface += " routed"
		}
		rows = append(rows, []string{nodeName(nodes, l.GetFrom()), nodeName(nodes, l.GetTo()), loud(l.GetClass()), fmt.Sprintf("%d µs", l.GetRttUs()), gbitsText(l.GetStreamBytesPerSecond()), gbitsText(l.GetAggregateBytesPerSecond()), iface, orDash(l.GetRdmaDevice()), when(l.GetMeasuredAt(), time.Kitchen), truncate(detail, 60)})
	}
	table(w, []string{"FROM", "TO", "CLASS", "RTT", "STREAM", "AGGREGATE", "IFACE", "RDMA", "MEASURED", "DETAIL"}, rows)
}

func gbitsText(bps uint64) string {
	if bps == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f Gb/s", float64(bps)*8/1e9)
}

func runMeshLinks(ctx context.Context, e *env, args []string) error {
	fs := e.flags("mesh links")
	probe := fs.Bool("probe", false, "measure the links now")
	force := fs.Bool("force", false, "with --probe, run the bandwidth measurements even while a formation is serving")
	positional, err := e.parse(fs, args, 0, 1, "mesh links [--probe] [--force] [node]")
	if err != nil {
		return err
	}
	nodes, err := e.cl.mesh.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{}))
	if err != nil {
		return err
	}
	if *probe {
		node := ""
		if len(positional) > 0 {
			node = positional[0]
		}
		resp, err := e.cl.mesh.Probe(ctx, connect.NewRequest(&v1.ProbeRequest{NodeId: node, Force: *force}))
		if err != nil {
			return err
		}
		return e.print(resp.Msg, func(w io.Writer) { linksTable(w, resp.Msg.GetLinks(), nodes.Msg.GetNodes()) })
	}
	links := allLinks(nodes.Msg.GetNodes())
	if len(positional) > 0 {
		var kept []*v1.Link
		for _, l := range links {
			if nodeName(nodes.Msg.GetNodes(), l.GetFrom()) == positional[0] || nodeName(nodes.Msg.GetNodes(), l.GetTo()) == positional[0] || l.GetFrom() == positional[0] || l.GetTo() == positional[0] {
				kept = append(kept, l)
			}
		}
		links = kept
	}
	return e.print(&v1.ProbeResponse{Links: links}, func(w io.Writer) { linksTable(w, links, nodes.Msg.GetNodes()) })
}

func runMeshProfiles(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("mesh profile"), args, 0, 0, "mesh profile list"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.GetMesh(ctx, connect.NewRequest(&v1.GetMeshRequest{}))
	if err != nil {
		return err
	}
	return e.print(&v1.SetDeviceProfileResponse{Profiles: resp.Msg.GetProfiles()}, func(w io.Writer) { profilesTable(w, resp.Msg.GetProfiles()) })
}

func profilesTable(w io.Writer, profiles []*v1.DeviceProfile) {
	var rows [][]string
	for _, p := range profiles {
		from := "set here"
		if p.GetUpdatedAt() != nil {
			from = "set here " + when(p.GetUpdatedAt(), time.DateOnly)
		}
		if p.GetBuiltin() {
			from = "shipped"
		}
		fixed := "-"
		if p.GetFixedSeconds() > 0 {
			fixed = fmt.Sprintf("%.1f ms", p.GetFixedSeconds()*1e3)
		}
		rows = append(rows, []string{p.GetPattern(), fmt.Sprintf("%.0f GB/s", p.GetStreamBytesPerSecond()/1e9), fmt.Sprintf("%.0f TFLOPS", p.GetComputeFlops()/1e12), fixed, from})
	}
	table(w, []string{"PATTERN", "STREAM", "COMPUTE", "FIXED", "FROM"}, rows)
}

func runMeshProfileSet(ctx context.Context, e *env, args []string) error {
	fs := e.flags("mesh profile set")
	stream := fs.Float64("stream", 0, "streaming bandwidth in GB/s")
	compute := fs.Float64("compute", 0, "dense compute in TFLOPS")
	fixed := fs.Float64("fixed", 0, "fixed cost per forward pass in ms")
	positional, err := e.parse(fs, args, 1, 1, "mesh profile set <device pattern> --stream <GB/s> --compute <TFLOPS> [--fixed <ms>]")
	if err != nil {
		return err
	}
	if *stream <= 0 {
		return fmt.Errorf("usage: nebu mesh profile set <device pattern> --stream <GB/s> --compute <TFLOPS>")
	}
	profile := &v1.DeviceProfile{Pattern: positional[0], StreamBytesPerSecond: *stream * 1e9, ComputeFlops: *compute * 1e12, FixedSeconds: *fixed / 1e3}
	resp, err := e.cl.mesh.SetDeviceProfile(ctx, connect.NewRequest(&v1.SetDeviceProfileRequest{Profile: profile}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { profilesTable(w, resp.Msg.GetProfiles()) })
}

func runMeshProfileDelete(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("mesh profile delete"), args, 1, 1, "mesh profile delete <device pattern>")
	if err != nil {
		return err
	}
	resp, err := e.cl.mesh.DeleteDeviceProfile(ctx, connect.NewRequest(&v1.DeleteDeviceProfileRequest{Pattern: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { profilesTable(w, resp.Msg.GetProfiles()) })
}

func runFormationsList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("formations")
	all := fs.Bool("all", false, "include stopped and failed formations")
	if _, err := e.parse(fs, args, 0, 0, "formations [--all]"); err != nil {
		return err
	}
	resp, err := e.cl.mesh.ListFormations(ctx, connect.NewRequest(&v1.ListFormationsRequest{RunningOnly: !*all}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { formationsTable(w, resp.Msg.GetFormations()) })
}

func formationsTable(w io.Writer, list []*v1.Formation) {
	var rows [][]string
	for _, f := range list {
		var seats []string
		for _, s := range f.GetSeats() {
			seats = append(seats, fmt.Sprintf("%s@%s", s.GetRole(), orDash(s.GetNodeName())))
		}
		tps := "-"
		if f.GetPlan().GetTokensPerSecond() > 0 {
			tps = fmt.Sprintf("%.1f", f.GetPlan().GetTokensPerSecond())
		}
		rows = append(rows, []string{f.GetId(), f.GetName(), loud(f.GetShape()), loud(f.GetState()), orDash(f.GetConductorName()), f.GetRuntimeId(), strings.Join(seats, " "), tps, truncate(f.GetError(), 50)})
	}
	table(w, []string{"ID", "NAME", "SHAPE", "STATE", "CONDUCTOR", "RUNTIME", "SEATS", "TPS", "DETAIL"}, rows)
}

func runFormationsShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("formations show"), args, 1, 1, "formations show <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.mesh.GetFormation(ctx, connect.NewRequest(&v1.GetFormationRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderFormation(w, resp.Msg.GetFormation()) })
}

func renderFormation(w io.Writer, f *v1.Formation) {
	fmt.Fprintf(w, "%s %s %s %s\n", f.GetId(), f.GetName(), loud(f.GetShape()), loud(f.GetState()))
	rows := [][]string{
		{"model", f.GetSourceId() + "/" + f.GetRepo() + " " + f.GetGroup()},
		{"runtime", f.GetRuntimeId()},
		{"conductor", f.GetConductorName() + " " + f.GetConductor()},
		{"endpoint", orDash(f.GetEndpoint())},
		{"created", when(f.GetCreatedAt(), time.RFC3339)},
		{"ready", when(f.GetReadyAt(), time.RFC3339)},
		{"stopped", when(f.GetStoppedAt(), time.RFC3339)},
		{"relaunch on daemon start", yes(f.GetDesiredRunning())},
		{"task", orDash(f.GetTaskId())},
	}
	if f.GetBytesMoved() > 0 {
		rows = append(rows, []string{"bytes moved to stages", estimate.Human(f.GetBytesMoved())})
	}
	if f.GetError() != "" {
		rows = append(rows, []string{"error", f.GetError()})
	}
	table(w, nil, rows)
	section(w, "seats")
	rows = nil
	for _, s := range f.GetSeats() {
		layers := "all"
		if s.GetLayerTo() > s.GetLayerFrom() {
			layers = fmt.Sprintf("%d-%d", s.GetLayerFrom(), s.GetLayerTo()-1)
		}
		rows = append(rows, []string{s.GetRole(), strconv.Itoa(int(s.GetRank())), orDash(s.GetNodeName()), loud(s.GetState()), layers, estimate.Human(s.GetWeightBytes()), estimate.Human(s.GetCacheBytes()), orDash(s.GetInstanceId()), orDash(s.GetEndpoint()), orDash(s.GetTransport()), truncate(s.GetError(), 40)})
	}
	table(w, []string{"ROLE", "RANK", "NODE", "STATE", "LAYERS", "WEIGHTS", "CACHE", "INSTANCE", "ENDPOINT", "TRANSPORT", "ERROR"}, rows)
	if plan := f.GetPlan(); plan != nil {
		section(w, "plan")
		fmt.Fprintln(w, plan.GetDetail())
		for _, src := range plan.GetSources() {
			fmt.Fprintln(w, "numbers:", src)
		}
		candidateTable(w, plan)
	}
}

// The planner's table: the shape chosen first, every rejected one with its reason
func candidateTable(w io.Writer, plan *v1.FormationPlan) {
	var rows [][]string
	for _, c := range plan.GetCandidates() {
		ttft, tps, rps, speedup := "-", "-", "-", "-"
		if c.GetPrefillSeconds() > 0 {
			ttft = fmt.Sprintf("%.2f s", c.GetPrefillSeconds())
		}
		if c.GetTokensPerSecond() > 0 {
			tps = fmt.Sprintf("%.1f", c.GetTokensPerSecond())
		}
		if c.GetRequestsPerSecond() > 0 {
			rps = fmt.Sprintf("%.2f", c.GetRequestsPerSecond())
		}
		if c.GetSpeedup() > 0 {
			speedup = fmt.Sprintf("%.2fx", c.GetSpeedup())
		}
		shape := loud(c.GetShape())
		if c.GetShape() == v1.Shape_SHAPE_UNSPECIFIED {
			shape = "-"
		}
		rows = append(rows, []string{shape, strings.Join(shortIDs(c.GetNodeIds()), ","), loud(c.GetVerdict()), ttft, tps, rps, speedup, c.GetReason()})
	}
	fmt.Fprintf(w, "at %d prompt and %d completion tokens\n", plan.GetReferencePrompt(), plan.GetReferenceCompletion())
	table(w, []string{"SHAPE", "NODES", "VERDICT", "TTFT", "TPS", "REQ/S", "SPEEDUP", "REASON"}, rows)
}

func shortIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if len(id) > 8 {
			id = id[:8]
		}
		out[i] = id
	}
	return out
}

func runFormationsStop(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("formations stop"), args, 1, 1, "formations stop <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.mesh.StopFormation(ctx, connect.NewRequest(&v1.StopFormationRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", resp.Msg.GetFormation().GetName(), text.Enum(resp.Msg.GetFormation().GetState()))
	})
}

func runFormationsLogs(ctx context.Context, e *env, args []string) error {
	fs := e.flags("formations logs")
	node := fs.String("node", "", "the seat's node, the head's when empty")
	role := fs.String("role", "", "the seat's role: head, stage, rank, prefill, decode, replica, denoiser")
	follow := fs.Bool("follow", false, "keep streaming until the seat exits")
	tail := fs.Uint("tail", 200, "lines to start from, 0 for everything retained")
	positional, err := e.parse(fs, args, 1, 1, "formations logs <name|id> [--node NODE] [--role ROLE] [--follow] [--tail N]")
	if err != nil {
		return err
	}
	stream, err := e.cl.mesh.FormationLogs(ctx, connect.NewRequest(&v1.FormationLogsRequest{Id: positional[0], NodeId: *node, Role: *role, Follow: *follow, Tail: uint32(*tail)}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		for _, line := range stream.Msg().GetLines() {
			fmt.Fprintln(e.out, line)
		}
	}
	return stream.Err()
}

// Parses a shape name
func parseShape(s string) (v1.Shape, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return v1.Shape_SHAPE_UNSPECIFIED, nil
	case "solo":
		return v1.Shape_SHAPE_SOLO, nil
	case "chain":
		return v1.Shape_SHAPE_CHAIN, nil
	case "lockstep":
		return v1.Shape_SHAPE_LOCKSTEP, nil
	case "relay":
		return v1.Shape_SHAPE_RELAY, nil
	case "replicas":
		return v1.Shape_SHAPE_REPLICAS, nil
	case "draft":
		return v1.Shape_SHAPE_DRAFT, nil
	case "stages":
		return v1.Shape_SHAPE_STAGES, nil
	}
	return 0, fmt.Errorf("shape %q is not auto, solo, chain, lockstep, relay, replicas, draft, or stages", s)
}

// Parses a plan profile name
func parseProfile(s string) (v1.PlanProfile, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "chat":
		return v1.PlanProfile_PLAN_PROFILE_CHAT, nil
	case "agent":
		return v1.PlanProfile_PLAN_PROFILE_AGENT, nil
	case "batch":
		return v1.PlanProfile_PLAN_PROFILE_BATCH, nil
	case "auto":
		return v1.PlanProfile_PLAN_PROFILE_AUTO, nil
	}
	return 0, fmt.Errorf("profile %q is not chat, agent, batch, or auto", s)
}

// Splits a comma separated span
func parseSpan(s string) []string {
	var out []string
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// Runs a formation across the span and follows it to ready
func (e *env) runFormation(ctx context.Context, req *v1.RunRequest) error {
	resp, err := e.cl.mesh.RunFormation(ctx, connect.NewRequest(&v1.RunFormationRequest{Run: req}))
	if err != nil {
		return err
	}
	f := resp.Msg.GetFormation()
	e.text("formation %s %s as %s across %d seats\n", f.GetId(), f.GetName(), text.Enum(f.GetShape()), len(f.GetSeats()))
	if plan := f.GetPlan(); plan != nil {
		e.text("plan %s\n", plan.GetDetail())
		if !e.json {
			candidateTable(e.out, plan)
		}
	}
	if _, err := e.follow(ctx, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := e.cl.mesh.GetFormation(ctx, connect.NewRequest(&v1.GetFormationRequest{Id: f.GetId()}))
	if err != nil {
		return err
	}
	if st := final.Msg.GetFormation(); st.GetState() != v1.FormationState_FORMATION_STATE_READY {
		return fmt.Errorf("%s is %s: %s", f.GetName(), text.Enum(st.GetState()), st.GetError())
	}
	return e.print(final.Msg, func(w io.Writer) { fmt.Fprintf(w, "gateway %s/v1 model %s\n", e.gatewayBase(ctx), f.GetName()) })
}

// Plans a repo's stored groups across the mesh and prints the fit table with every shape
func (e *env) inspectMesh(ctx context.Context, source, repo string, groups []string, runtimeID string, params map[string]string, span []string, shape v1.Shape, profile v1.PlanProfile) error {
	mesh, err := e.cl.mesh.GetMesh(ctx, connect.NewRequest(&v1.GetMeshRequest{}))
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, g := range groups {
		want[g] = true
	}
	found := map[string]string{}
	var order []string
	for _, n := range mesh.Msg.GetNodes() {
		for _, s := range n.GetStored() {
			if s.GetRepo() != repo || source != "" && s.GetSourceId() != source || len(want) > 0 && !want[s.GetGroup()] {
				continue
			}
			if _, ok := found[s.GetGroup()]; !ok {
				found[s.GetGroup()] = s.GetSourceId()
				order = append(order, s.GetGroup())
			}
		}
	}
	if len(order) == 0 {
		return fmt.Errorf("%s is stored on no member of the mesh, pull it first", repo)
	}
	sort.Strings(order)
	out := &v1.GetMeshResponse{}
	for _, group := range order {
		resp, err := e.cl.mesh.PlanFormation(ctx, connect.NewRequest(&v1.PlanFormationRequest{Run: &v1.RunRequest{SourceId: found[group], Repo: repo, Group: group, RuntimeId: runtimeID, Params: params, Span: span, Shape: shape, Profile: profile}}))
		if err != nil {
			return fmt.Errorf("%s: %w", group, err)
		}
		plan := resp.Msg.GetPlan()
		out.Formations = append(out.Formations, &v1.Formation{Name: repo + ":" + group, Group: group, Repo: repo, Plan: plan, Shape: plan.GetShape(), RuntimeId: plan.GetRuntimeId()})
		if !e.json {
			section(e.out, group+" on "+plan.GetRuntimeId())
			fmt.Fprintln(e.out, plan.GetDetail())
			for _, src := range plan.GetSources() {
				fmt.Fprintln(e.out, "numbers:", src)
			}
			candidateTable(e.out, plan)
		}
	}
	if e.json {
		return e.print(out, nil)
	}
	return nil
}

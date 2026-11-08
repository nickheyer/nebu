package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// The class each procedure belongs to, written apart from the guard's own table
var wantClass = map[string]string{
	nebuv1connect.AuthServiceListUsersProcedure:                "admin",
	nebuv1connect.AuthServiceCreateUserProcedure:               "admin",
	nebuv1connect.AuthServiceDeleteUserProcedure:               "admin",
	nebuv1connect.AuthServiceSetPasswordProcedure:              "inference",
	nebuv1connect.AuthServiceListTokensProcedure:               "inference",
	nebuv1connect.AuthServiceCreateTokenProcedure:              "inference",
	nebuv1connect.AuthServiceDeleteTokenProcedure:              "inference",
	nebuv1connect.BotServiceListBotsProcedure:                  "read",
	nebuv1connect.BotServiceGetBotProcedure:                    "read",
	nebuv1connect.BotServiceListBotActivityProcedure:           "read",
	nebuv1connect.BotServiceListBotGuildsProcedure:             "read",
	nebuv1connect.BotServiceCreateBotProcedure:                 "operate",
	nebuv1connect.BotServiceUpdateBotProcedure:                 "operate",
	nebuv1connect.BotServiceDeleteBotProcedure:                 "operate",
	nebuv1connect.BotServiceStartBotProcedure:                  "operate",
	nebuv1connect.BotServiceStopBotProcedure:                   "operate",
	nebuv1connect.BotServiceProbeBotTokenProcedure:             "operate",
	nebuv1connect.BotServiceSendBotMessageProcedure:            "operate",
	nebuv1connect.BuildServiceListRecipesProcedure:             "read",
	nebuv1connect.BuildServiceListBuildsProcedure:              "read",
	nebuv1connect.BuildServiceGetBuildProcedure:                "read",
	nebuv1connect.BuildServiceBuildProcedure:                   "operate",
	nebuv1connect.BuildServiceRemoveBuildProcedure:             "operate",
	nebuv1connect.ChatServiceListConversationsProcedure:        "inference",
	nebuv1connect.ChatServiceGetConversationProcedure:          "inference",
	nebuv1connect.ChatServicePutConversationProcedure:          "inference",
	nebuv1connect.ChatServiceRenameConversationProcedure:       "inference",
	nebuv1connect.ChatServiceDeleteConversationProcedure:       "inference",
	nebuv1connect.ChatServicePutChatFileProcedure:              "inference",
	nebuv1connect.ChatServiceGetChatFileProcedure:              "inference",
	nebuv1connect.ChatServiceDeleteChatFilesProcedure:          "inference",
	nebuv1connect.EstimateServiceInspectProcedure:              "read",
	nebuv1connect.EstimateServiceEstimateProcedure:             "read",
	nebuv1connect.EstimateServicePartsProcedure:                "read",
	nebuv1connect.EventServiceWatchEventsProcedure:             "read",
	nebuv1connect.GatewayServiceGetGatewayStatusProcedure:      "read",
	nebuv1connect.GatewayServiceListRoutesProcedure:            "read",
	nebuv1connect.GatewayServiceGetRouteProcedure:              "read",
	nebuv1connect.GatewayServiceListPoolsProcedure:             "read",
	nebuv1connect.GatewayServiceGetPoolProcedure:               "read",
	nebuv1connect.GatewayServiceListObjectivesProcedure:        "read",
	nebuv1connect.GatewayServiceListSchedulerConfigsProcedure:  "read",
	nebuv1connect.GatewayServiceListTracesProcedure:            "read",
	nebuv1connect.GatewayServiceGetTraceProcedure:              "read",
	nebuv1connect.GatewayServicePutRouteProcedure:              "operate",
	nebuv1connect.GatewayServiceDeleteRouteProcedure:           "operate",
	nebuv1connect.GatewayServicePutPoolProcedure:               "operate",
	nebuv1connect.GatewayServiceDeletePoolProcedure:            "operate",
	nebuv1connect.GatewayServicePutObjectiveProcedure:          "operate",
	nebuv1connect.GatewayServiceDeleteObjectiveProcedure:       "operate",
	nebuv1connect.GatewayServicePutSchedulerConfigProcedure:    "operate",
	nebuv1connect.GatewayServiceDeleteSchedulerConfigProcedure: "operate",
	nebuv1connect.HostServiceGetProfileProcedure:               "read",
	nebuv1connect.HostServiceDoctorProcedure:                   "read",
	nebuv1connect.HostServiceLogsProcedure:                     "read",
	nebuv1connect.HostServiceListDirectoryProcedure:            "operate",
	nebuv1connect.InstanceServiceListInstancesProcedure:        "read",
	nebuv1connect.InstanceServiceGetInstanceProcedure:          "read",
	nebuv1connect.InstanceServiceLogsProcedure:                 "read",
	nebuv1connect.InstanceServicePreviewRunProcedure:           "read",
	nebuv1connect.InstanceServiceRunProcedure:                  "operate",
	nebuv1connect.InstanceServiceStopInstanceProcedure:         "operate",
	nebuv1connect.InstanceServiceLoadAdapterProcedure:          "operate",
	nebuv1connect.InstanceServiceUnloadAdapterProcedure:        "operate",
	nebuv1connect.InstanceServiceListAdaptersProcedure:         "read",
	nebuv1connect.MeshServiceInitProcedure:                     "admin",
	nebuv1connect.MeshServiceJoinProcedure:                     "admin",
	nebuv1connect.MeshServiceLeaveProcedure:                    "admin",
	nebuv1connect.MeshServiceResetMeshProcedure:                "admin",
	nebuv1connect.MeshServiceRotateProcedure:                   "admin",
	nebuv1connect.MeshServiceTokenProcedure:                    "admin",
	nebuv1connect.MeshServiceInviteProcedure:                   "admin",
	nebuv1connect.MeshServiceAskProcedure:                      "admin",
	nebuv1connect.MeshServiceAdmitProcedure:                    "admin",
	nebuv1connect.MeshServiceRefuseProcedure:                   "admin",
	nebuv1connect.MeshServiceHandOffIssuerProcedure:            "admin",
	nebuv1connect.MeshServiceDismissProcedure:                  "admin",
	nebuv1connect.MeshServiceExportTokenProcedure:              "admin",
	nebuv1connect.MeshServiceImportMeshProcedure:               "admin",
	nebuv1connect.MeshServiceDropFederationProcedure:           "admin",
	nebuv1connect.MeshServiceGetMeshProcedure:                  "read",
	nebuv1connect.MeshServiceListNodesProcedure:                "read",
	nebuv1connect.MeshServiceDiscoverProcedure:                 "read",
	nebuv1connect.MeshServicePlanFormationProcedure:            "read",
	nebuv1connect.MeshServiceListFormationsProcedure:           "read",
	nebuv1connect.MeshServiceGetFormationProcedure:             "read",
	nebuv1connect.MeshServiceFormationLogsProcedure:            "read",
	nebuv1connect.MeshServiceProbeProcedure:                    "operate",
	nebuv1connect.MeshServiceSetDeviceProfileProcedure:         "operate",
	nebuv1connect.MeshServiceDeleteDeviceProfileProcedure:      "operate",
	nebuv1connect.MeshServiceRunFormationProcedure:             "operate",
	nebuv1connect.MeshServiceStopFormationProcedure:            "operate",
	nebuv1connect.MeshServiceDeleteFormationProcedure:          "operate",
	nebuv1connect.MeshServiceForgetNodeProcedure:               "peer",
	nebuv1connect.MeshServiceRunSeatProcedure:                  "peer",
	nebuv1connect.MeshServiceStopSeatProcedure:                 "peer",
	nebuv1connect.MeshServiceMoveSlotProcedure:                 "peer",
	nebuv1connect.MeshServiceDropSlotProcedure:                 "peer",
	nebuv1connect.MeshServiceGetSeatProcedure:                  "peer",
	nebuv1connect.MeshServicePullSeatProcedure:                 "peer",
	nebuv1connect.MeshServiceWatchPullProcedure:                "peer",
	nebuv1connect.MeshServiceSeatLogsProcedure:                 "peer",
	nebuv1connect.MeshServiceGetStoredProcedure:                "peer",
	nebuv1connect.MeshPeerServiceKnockProcedure:                "open",
	nebuv1connect.MeshPeerServiceOfferProcedure:                "open",
	nebuv1connect.MeshPeerServiceWelcomeProcedure:              "open",
	nebuv1connect.MeshPeerServiceEnrollProcedure:               "open",
	nebuv1connect.MeshPeerServiceFederateProcedure:             "open",
	nebuv1connect.MeshPeerServiceExportsProcedure:              "open",
	nebuv1connect.MeshPeerServiceRenewProcedure:                "peer",
	nebuv1connect.MeshPeerServiceGrantProcedure:                "peer",
	nebuv1connect.MeshPeerServiceReissueProcedure:              "peer",
	nebuv1connect.MeshPeerServiceTakeIssuerProcedure:           "peer",
	nebuv1connect.MeshPeerServiceSyncProcedure:                 "peer",
	nebuv1connect.MeshPeerServiceByeProcedure:                  "peer",
	nebuv1connect.MeshPeerServiceStreamProcedure:               "peer",
	nebuv1connect.RuntimeServiceListFormatsProcedure:           "read",
	nebuv1connect.RuntimeServiceListRuntimesProcedure:          "read",
	nebuv1connect.RuntimeServiceGetRuntimeProcedure:            "read",
	nebuv1connect.RuntimeServiceListInstallsProcedure:          "read",
	nebuv1connect.RuntimeServiceAdoptInstallProcedure:          "operate",
	nebuv1connect.RuntimeServiceInstallProcedure:               "operate",
	nebuv1connect.RuntimeServiceRemoveInstallProcedure:         "operate",
	nebuv1connect.SettingsServiceGetSettingsProcedure:          "admin",
	nebuv1connect.SettingsServiceUpdateSettingsProcedure:       "admin",
	nebuv1connect.SlotServiceListSlotsProcedure:                "read",
	nebuv1connect.SlotServiceGetSlotProcedure:                  "read",
	nebuv1connect.SlotServiceCreateSlotProcedure:               "operate",
	nebuv1connect.SlotServiceUpdateSlotProcedure:               "operate",
	nebuv1connect.SlotServiceDeleteSlotProcedure:               "operate",
	nebuv1connect.SlotServiceSwapProcedure:                     "operate",
	nebuv1connect.SlotServiceEvictSlotProcedure:                "operate",
	nebuv1connect.SlotServiceRelaunchSlotProcedure:             "operate",
	nebuv1connect.SourceServiceListSourcesProcedure:            "read",
	nebuv1connect.SourceServiceListProvidersProcedure:          "read",
	nebuv1connect.SourceServiceSearchProcedure:                 "read",
	nebuv1connect.SourceServiceResolveProcedure:                "read",
	nebuv1connect.SourceServiceListRevisionsProcedure:          "read",
	nebuv1connect.SourceServiceGetModelCardProcedure:           "read",
	nebuv1connect.SourceServiceCreateSourceProcedure:           "admin",
	nebuv1connect.SourceServiceUpdateSourceProcedure:           "admin",
	nebuv1connect.SourceServiceDeleteSourceProcedure:           "admin",
	nebuv1connect.StoreServiceListModelsProcedure:              "read",
	nebuv1connect.StoreServiceGetModelProcedure:                "read",
	nebuv1connect.StoreServiceGetStatusProcedure:               "read",
	nebuv1connect.StoreServicePullProcedure:                    "operate",
	nebuv1connect.StoreServiceRemoveModelProcedure:             "operate",
	nebuv1connect.StoreServiceGcProcedure:                      "operate",
	nebuv1connect.StoreServiceVerifyProcedure:                  "operate",
	nebuv1connect.StoreServiceExportProcedure:                  "operate",
	nebuv1connect.TaskServiceListTasksProcedure:                "read",
	nebuv1connect.TaskServiceGetTaskProcedure:                  "read",
	nebuv1connect.TaskServiceWatchTaskProcedure:                "read",
	nebuv1connect.TaskServiceCancelTaskProcedure:               "operate",
	nebuv1connect.ToolServiceListToolServersProcedure:          "peerInference",
	nebuv1connect.ToolServiceCallToolProcedure:                 "peerInference",
	nebuv1connect.PoolImportServiceListPoolImportsProcedure:    "read",
	nebuv1connect.PoolImportServiceGetPoolImportProcedure:      "read",
	nebuv1connect.PoolImportServiceWatchPoolImportsProcedure:   "read",
}

// Every procedure the generated nebu.v1 services declare
func registeredProcedures() []string {
	var out []string
	protoregistry.GlobalFiles.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		if string(f.Package()) != "nebu.v1" {
			return true
		}
		for i := 0; i < f.Services().Len(); i++ {
			svc := f.Services().Get(i)
			for j := 0; j < svc.Methods().Len(); j++ {
				out = append(out, "/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name()))
			}
		}
		return true
	})
	slices.Sort(out)
	return out
}

// An account with a token per scope, the daemon token, and a member session, each as request headers
type creds struct {
	guard  *Guard
	byName map[string]http.Header
}

func scopedCreds(t *testing.T) creds {
	t.Helper()
	users, sessions := openUsers(t)
	ctx := context.Background()
	ann, err := users.Setup(ctx, "ann", "ann-password")
	if err != nil {
		t.Fatal(err)
	}
	me := Identity{Subject: ann.GetId(), Provider: ProviderLocal, Name: "ann", Role: v1.Role_ROLE_ADMIN, Scopes: RoleScopes(v1.Role_ROLE_ADMIN)}
	guard := NewGuard("daemon-token", sessions, users.tokens)
	guard.SetPeers(onePeer{token: "member-session", node: "peer"})
	c := creds{guard: guard, byName: map[string]http.Header{"none": {}, "peer": bearer("member-session"), "daemon": bearer("daemon-token")}}
	for _, s := range []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ, v1.Scope_SCOPE_OPERATE, v1.Scope_SCOPE_ADMIN} {
		tok, err := users.tokens.Create(ctx, me, ScopeName(s), []v1.Scope{s})
		if err != nil {
			t.Fatal(err)
		}
		c.byName[ScopeName(s)] = bearer(tok.GetSecret())
	}
	return c
}

func bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

// The mesh session table as the guard sees it, one member's token
type onePeer struct{ token, node string }

func (p onePeer) Peer(token string) (string, bool) {
	if token == p.token {
		return p.node, true
	}
	return "", false
}

// What each credential gets per class, ok, unauthenticated, or denied
var outcomes = map[string]map[string]connect.Code{
	"open":          {},
	"inference":     {"none": connect.CodeUnauthenticated, "peer": connect.CodeUnauthenticated, "read": connect.CodePermissionDenied, "operate": connect.CodePermissionDenied},
	"read":          {"none": connect.CodeUnauthenticated, "peer": connect.CodeUnauthenticated, "inference": connect.CodePermissionDenied},
	"operate":       {"none": connect.CodeUnauthenticated, "peer": connect.CodeUnauthenticated, "inference": connect.CodePermissionDenied, "read": connect.CodePermissionDenied},
	"admin":         {"none": connect.CodeUnauthenticated, "peer": connect.CodeUnauthenticated, "inference": connect.CodePermissionDenied, "read": connect.CodePermissionDenied, "operate": connect.CodePermissionDenied},
	"peer":          {"none": connect.CodeUnauthenticated, "inference": connect.CodePermissionDenied, "read": connect.CodePermissionDenied, "operate": connect.CodePermissionDenied},
	"peerInference": {"none": connect.CodeUnauthenticated, "read": connect.CodePermissionDenied, "operate": connect.CodePermissionDenied},
}

func TestEveryProcedureHasItsScope(t *testing.T) {
	registered := registeredProcedures()
	if len(registered) == 0 {
		t.Fatal("no procedures registered")
	}
	for _, p := range registered {
		if _, ok := wantClass[p]; !ok {
			t.Errorf("%s has no class in the test table", p)
		}
		if _, ok := procedures[p]; !ok {
			t.Errorf("%s has no rule in the guard", p)
		}
	}
	for p := range wantClass {
		if !slices.Contains(registered, p) {
			t.Errorf("%s is in the test table but no service declares it", p)
		}
	}
	for p := range procedures {
		if !slices.Contains(registered, p) {
			t.Errorf("%s has a rule but no service declares it", p)
		}
	}
	c := scopedCreds(t)
	for _, p := range registered {
		class := wantClass[p]
		for name, h := range c.byName {
			want := outcomes[class][name]
			got := connect.CodeOf(c.guard.Check(p, h))
			if c.guard.Check(p, h) == nil {
				got = 0
			}
			if got != want {
				t.Errorf("%s (%s) with %s credential gave %v, want %v", p, class, name, got, want)
			}
		}
	}
	if err := c.guard.Check("/nebu.v1.Nothing/Here", bearer("daemon-token")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a procedure without a rule should be refused %v", err)
	}
	off := NewGuard("", nil, nil)
	if err := off.Check(nebuv1connect.SettingsServiceUpdateSettingsProcedure, http.Header{}); err != nil {
		t.Fatalf("auth off passes every known procedure %v", err)
	}
	if err := off.Check("/nebu.v1.Nothing/Here", http.Header{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("auth off still refuses unknown procedures %v", err)
	}
}

// The interceptor refuses unary and streaming calls a credential's scopes do not open
func TestInterceptorGuardsHandlers(t *testing.T) {
	c := scopedCreds(t)
	opts := connect.WithInterceptors(c.guard.Interceptor())
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewSettingsServiceHandler(nebuv1connect.UnimplementedSettingsServiceHandler{}, opts))
	mux.Handle(nebuv1connect.NewEventServiceHandler(nebuv1connect.UnimplementedEventServiceHandler{}, opts))
	mux.Handle(nebuv1connect.NewPoolImportServiceHandler(nebuv1connect.UnimplementedPoolImportServiceHandler{}, opts))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	settings := nebuv1connect.NewSettingsServiceClient(srv.Client(), srv.URL)
	events := nebuv1connect.NewEventServiceClient(srv.Client(), srv.URL)
	for _, cs := range []struct {
		cred          string
		unary, stream connect.Code
	}{
		{"none", connect.CodeUnauthenticated, connect.CodeUnauthenticated},
		{"inference", connect.CodePermissionDenied, connect.CodePermissionDenied},
		{"read", connect.CodePermissionDenied, connect.CodeUnimplemented},
		{"operate", connect.CodePermissionDenied, connect.CodeUnimplemented},
		{"admin", connect.CodeUnimplemented, connect.CodeUnimplemented},
		{"daemon", connect.CodeUnimplemented, connect.CodeUnimplemented},
	} {
		req := connect.NewRequest(&v1.GetSettingsRequest{})
		for k, v := range c.byName[cs.cred] {
			req.Header()[k] = v
		}
		if _, err := settings.GetSettings(ctx, req); connect.CodeOf(err) != cs.unary {
			t.Errorf("unary with %s gave %v, want %v", cs.cred, err, cs.unary)
		}
		sreq := connect.NewRequest(&v1.WatchEventsRequest{})
		for k, v := range c.byName[cs.cred] {
			sreq.Header()[k] = v
		}
		stream, err := events.WatchEvents(ctx, sreq)
		if err == nil {
			stream.Receive()
			err = stream.Err()
			stream.Close()
		}
		if connect.CodeOf(err) != cs.stream {
			t.Errorf("stream with %s gave %v, want %v", cs.cred, err, cs.stream)
		}
	}
}

// Sends one settings event then one task event
type twoEvents struct {
	nebuv1connect.UnimplementedEventServiceHandler
}

func (twoEvents) WatchEvents(_ context.Context, _ *connect.Request[v1.WatchEventsRequest], stream *connect.ServerStream[v1.WatchEventsResponse]) error {
	for _, kind := range []v1.EventKind{v1.EventKind_EVENT_KIND_SETTINGS, v1.EventKind_EVENT_KIND_TASK} {
		if err := stream.Send(&v1.WatchEventsResponse{Event: &v1.Event{Kind: kind}}); err != nil {
			return err
		}
	}
	return nil
}

// Settings events reach admin subscribers alone, as GetSettings does
func TestEventStreamDropsSettingsWithoutAdmin(t *testing.T) {
	c := scopedCreds(t)
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewEventServiceHandler(twoEvents{}, connect.WithInterceptors(c.guard.Interceptor())))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	events := nebuv1connect.NewEventServiceClient(srv.Client(), srv.URL)
	for cred, want := range map[string][]v1.EventKind{
		"read":   {v1.EventKind_EVENT_KIND_TASK},
		"admin":  {v1.EventKind_EVENT_KIND_SETTINGS, v1.EventKind_EVENT_KIND_TASK},
		"daemon": {v1.EventKind_EVENT_KIND_SETTINGS, v1.EventKind_EVENT_KIND_TASK},
	} {
		req := connect.NewRequest(&v1.WatchEventsRequest{})
		for k, v := range c.byName[cred] {
			req.Header()[k] = v
		}
		stream, err := events.WatchEvents(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var got []v1.EventKind
		for stream.Receive() {
			got = append(got, stream.Msg().GetEvent().GetKind())
		}
		stream.Close()
		if err := stream.Err(); err != nil || !slices.Equal(got, want) {
			t.Fatalf("%s received %v %v, want %v", cred, got, err, want)
		}
	}
}

// Sends one trace event per owner it is given
type ownedTraces []string

func (o ownedTraces) WatchEvents(_ context.Context, _ *connect.Request[v1.WatchEventsRequest], stream *connect.ServerStream[v1.WatchEventsResponse]) error {
	for _, owner := range o {
		if err := stream.Send(&v1.WatchEventsResponse{Event: &v1.Event{Kind: v1.EventKind_EVENT_KIND_TRACE, Id: owner, Payload: &v1.Event_Trace{Trace: &v1.Trace{Id: owner, Owner: owner}}}}); err != nil {
			return err
		}
	}
	return nil
}

// Trace events reach their owner and admins, other accounts' traces never reaching a non admin subscriber
func TestEventStreamScopesTracesByOwner(t *testing.T) {
	users, sessions := openUsers(t)
	ctx := context.Background()
	ann, err := users.Setup(ctx, "ann", "ann-password")
	if err != nil {
		t.Fatal(err)
	}
	annID := Identity{Subject: ann.GetId(), Provider: ProviderLocal, Name: "ann", Role: v1.Role_ROLE_ADMIN, Scopes: RoleScopes(v1.Role_ROLE_ADMIN)}
	bob, err := users.Create(ctx, annID, "bob", "bob-password", v1.Role_ROLE_OPERATOR)
	if err != nil {
		t.Fatal(err)
	}
	bobID := Identity{Subject: bob.GetId(), Provider: ProviderLocal, Name: "bob", Role: v1.Role_ROLE_OPERATOR, Scopes: RoleScopes(v1.Role_ROLE_OPERATOR)}
	byName := map[string]http.Header{"daemon": bearer("daemon-token")}
	for name, tc := range map[string]struct {
		who   Identity
		scope v1.Scope
	}{
		"ann-read":  {annID, v1.Scope_SCOPE_READ},
		"ann-admin": {annID, v1.Scope_SCOPE_ADMIN},
		"bob-read":  {bobID, v1.Scope_SCOPE_READ},
	} {
		tok, err := users.tokens.Create(ctx, tc.who, name, []v1.Scope{tc.scope})
		if err != nil {
			t.Fatal(err)
		}
		byName[name] = bearer(tok.GetSecret())
	}
	guard := NewGuard("daemon-token", sessions, users.tokens)
	mux := http.NewServeMux()
	mux.Handle(nebuv1connect.NewEventServiceHandler(ownedTraces{ann.GetId(), bob.GetId(), "stranger"}, connect.WithInterceptors(guard.Interceptor())))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	events := nebuv1connect.NewEventServiceClient(srv.Client(), srv.URL)
	for _, tc := range []struct {
		cred string
		want []string
	}{
		{"ann-read", []string{ann.GetId()}},
		{"bob-read", []string{bob.GetId()}},
		{"ann-admin", []string{ann.GetId(), bob.GetId(), "stranger"}},
		{"daemon", []string{ann.GetId(), bob.GetId(), "stranger"}},
	} {
		req := connect.NewRequest(&v1.WatchEventsRequest{})
		for k, v := range byName[tc.cred] {
			req.Header()[k] = v
		}
		stream, err := events.WatchEvents(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for stream.Receive() {
			got = append(got, stream.Msg().GetEvent().GetTrace().GetOwner())
		}
		stream.Close()
		if err := stream.Err(); err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("%s received %v %v, want %v", tc.cred, got, err, tc.want)
		}
	}
	off := NewGuard("", nil, nil)
	if !off.receives(http.Header{}, &v1.Event{Kind: v1.EventKind_EVENT_KIND_TRACE, Payload: &v1.Event_Trace{Trace: &v1.Trace{Owner: "anyone"}}}) {
		t.Fatal("a trace was dropped while auth is off")
	}
}

// Tokens are nebu_, a prefix, and a base62 secret, stored as prefix and hash and matched by hash
func TestTokensHashedRoundTrip(t *testing.T) {
	users, _ := openUsers(t)
	tokens := users.tokens
	ctx := context.Background()
	ann, err := users.Setup(ctx, "ann", "ann-password")
	if err != nil {
		t.Fatal(err)
	}
	me := Identity{Subject: ann.GetId(), Provider: ProviderLocal, Name: "ann", Role: v1.Role_ROLE_ADMIN, Scopes: RoleScopes(v1.Role_ROLE_ADMIN)}
	tok, err := tokens.Create(ctx, me, " laptop ", []v1.Scope{v1.Scope_SCOPE_READ, v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ})
	if err != nil {
		t.Fatal(err)
	}
	whole := tok.GetSecret()
	if len(whole) != len(tokenMark)+prefixLen+secretLen || !strings.HasPrefix(whole, tokenMark+tok.GetPrefix()) || len(tok.GetPrefix()) != prefixLen {
		t.Fatalf("token form %q prefix %q", whole, tok.GetPrefix())
	}
	for _, r := range whole[len(tokenMark):] {
		if !strings.ContainsRune(base62, r) {
			t.Fatalf("%q is not base62", whole)
		}
	}
	if tok.GetName() != "laptop" || !slices.Equal(tok.GetScopes(), []v1.Scope{v1.Scope_SCOPE_INFERENCE, v1.Scope_SCOPE_READ}) {
		t.Fatalf("token %v", tok)
	}
	row, err := users.db.FindToken(ctx, tok.GetPrefix())
	if err != nil || row.Hash != db.TokenHash(whole) || row.Token.GetSecret() != "" || strings.Contains(row.Hash, whole) {
		t.Fatalf("stored %+v %v", row, err)
	}
	listed, _ := tokens.List(ctx, me)
	if len(listed) != 1 || listed[0].GetSecret() != "" || listed[0].GetPrefix() != tok.GetPrefix() || listed[0].GetName() != "laptop" || len(listed[0].GetScopes()) != 2 || listed[0].GetCreatedAt() == nil {
		t.Fatalf("list %v", listed)
	}
	id, ok := tokens.Identify(ctx, whole)
	if !ok || id.Subject != ann.GetId() || id.TokenPrefix != tok.GetPrefix() || id.Role != v1.Role_ROLE_ADMIN || !slices.Equal(id.Scopes, tok.GetScopes()) {
		t.Fatalf("identify %+v %v", id, ok)
	}
	forged := tokenMark + tok.GetPrefix() + strings.Repeat("A", secretLen)
	if _, ok := tokens.Identify(ctx, forged); ok {
		t.Fatal("the right prefix with a wrong secret passed")
	}
	if _, ok := tokens.Identify(ctx, "short"); ok {
		t.Fatal("a token shorter than a prefix passed")
	}
	// A token from before hashing answers under its first twelve characters
	legacy := "Zk3x9_QmA-bc8yTq2LwPpR0sUvNnJ4hEoDgFiKa7XYz"
	all := RoleScopes(v1.Role_ROLE_ADMIN)
	if err := users.db.PutToken(ctx, db.TokenRow{Token: &v1.ApiToken{Id: "old", Name: "old", Prefix: legacy[:prefixLen], Scopes: all}, Hash: db.TokenHash(legacy), Role: v1.Role_ROLE_ADMIN, Provider: ProviderLocal, Subject: ann.GetId(), Owner: "ann"}); err != nil {
		t.Fatal(err)
	}
	if id, ok := tokens.Identify(ctx, legacy); !ok || id.TokenPrefix != legacy[:prefixLen] || !slices.Equal(id.Scopes, all) {
		t.Fatalf("legacy %+v %v", id, ok)
	}
	// Scopes beyond the role the row was made under are dropped when it answers
	if err := users.db.PutToken(ctx, db.TokenRow{Token: &v1.ApiToken{Id: "capped", Name: "capped", Prefix: "cappedprefix", Scopes: all}, Hash: db.TokenHash(tokenMark + "cappedprefix" + strings.Repeat("z", secretLen)), Role: v1.Role_ROLE_INFERENCE, Provider: ProviderLocal, Subject: ann.GetId(), Owner: "ann"}); err != nil {
		t.Fatal(err)
	}
	if id, ok := tokens.Identify(ctx, tokenMark+"cappedprefix"+strings.Repeat("z", secretLen)); !ok || !slices.Equal(id.Scopes, []v1.Scope{v1.Scope_SCOPE_INFERENCE}) {
		t.Fatalf("capped %+v %v", id, ok)
	}
}

func TestGuardIdentify(t *testing.T) {
	users, sessions := openUsers(t)
	ctx := context.Background()
	ann, _ := users.Setup(ctx, "ann", "ann-password")
	me := Identity{Subject: ann.GetId(), Provider: ProviderLocal, Name: "ann", Role: v1.Role_ROLE_ADMIN, Scopes: RoleScopes(v1.Role_ROLE_ADMIN)}
	tok, err := users.tokens.Create(ctx, me, "ci", []v1.Scope{v1.Scope_SCOPE_INFERENCE})
	if err != nil {
		t.Fatal(err)
	}
	guard := NewGuard("daemon-token", sessions, users.tokens)
	if id, ok := guard.Identify(bearer("daemon-token")); !ok || id.Provider != ProviderDaemon || id.Subject != "" || !id.Admin() || id.TokenPrefix != "" {
		t.Fatalf("daemon %+v %v", id, ok)
	}
	id, ok := guard.Identify(http.Header{"X-Api-Key": {tok.GetSecret()}})
	if !ok || id.Subject != ann.GetId() || id.Name != "ann" || id.TokenPrefix != tok.GetPrefix() || id.Role != v1.Role_ROLE_ADMIN || !slices.Equal(id.Scopes, []v1.Scope{v1.Scope_SCOPE_INFERENCE}) {
		t.Fatalf("api token %+v %v", id, ok)
	}
	if id.Admin() || !guard.Scoped(bearer(tok.GetSecret()), v1.Scope_SCOPE_INFERENCE) || guard.Scoped(bearer(tok.GetSecret()), v1.Scope_SCOPE_READ) {
		t.Fatal("an inference token opens inference alone")
	}
	rec := httptest.NewRecorder()
	sessions.Issue(rec, httptest.NewRequest(http.MethodGet, "/", nil), Session{Subject: ann.GetId(), Name: "ann", Provider: ProviderLocal, Role: v1.Role_ROLE_ADMIN})
	cookie := http.Header{"Cookie": {strings.Split(rec.Header().Get("Set-Cookie"), ";")[0]}}
	if id, ok := guard.Identify(cookie); !ok || id.Subject != ann.GetId() || id.TokenPrefix != "" || !slices.Equal(id.Scopes, RoleScopes(v1.Role_ROLE_ADMIN)) {
		t.Fatalf("session %+v %v", id, ok)
	}
	if _, ok := guard.Identify(bearer("nope")); ok || guard.Scoped(http.Header{}, v1.Scope_SCOPE_INFERENCE) {
		t.Fatal("nothing valid identifies nobody")
	}
	off := NewGuard("", nil, nil)
	if _, ok := off.Identify(bearer("anything")); ok || !off.Scoped(http.Header{}, v1.Scope_SCOPE_ADMIN) {
		t.Fatal("auth off identifies nobody and passes every scope")
	}
}

// Roles bound who changes, adds, and removes accounts and what scopes their tokens hold
func TestAccountRules(t *testing.T) {
	users, _ := openUsers(t)
	ctx := context.Background()
	ann, _ := users.Setup(ctx, "ann", "ann-password")
	admin := Identity{Subject: ann.GetId(), Provider: ProviderLocal, Name: "ann", Role: v1.Role_ROLE_ADMIN, Scopes: RoleScopes(v1.Role_ROLE_ADMIN)}
	if _, err := users.Setup(ctx, "eve", "eve-password"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("second setup %v", err)
	}
	bob, err := users.Create(ctx, admin, "bob", "bob-password", v1.Role_ROLE_OPERATOR)
	if err != nil {
		t.Fatal(err)
	}
	operator := Identity{Subject: bob.GetId(), Provider: ProviderLocal, Name: "bob", Role: v1.Role_ROLE_OPERATOR, Scopes: RoleScopes(v1.Role_ROLE_OPERATOR)}
	if _, err := users.Create(ctx, admin, "carl", "carl-password", v1.Role_ROLE_INFERENCE); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(ctx, operator, "dora", "dora-password", v1.Role_ROLE_INFERENCE); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator adding an account %v", err)
	}
	if _, err := users.Create(ctx, admin, "dora", "dora-password", v1.Role(42)); !errors.Is(err, ErrUser) {
		t.Fatalf("unknown role %v", err)
	}
	// An admin token without the admin scope is no admin for account changes
	adminRead := admin
	adminRead.Scopes = []v1.Scope{v1.Scope_SCOPE_READ}
	if _, err := users.Create(ctx, adminRead, "dora", "dora-password", v1.Role_ROLE_INFERENCE); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read scoped admin adding an account %v", err)
	}
	if _, err := users.SetPassword(ctx, operator, "carl", "bob-password", "new-carl-pass"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator changing another's password %v", err)
	}
	if _, err := users.SetPassword(ctx, operator, "bob", "wrong-password", "new-bob-pass"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("own password with a wrong current one %v", err)
	}
	if _, err := users.SetPassword(ctx, operator, "BOB", "bob-password", "new-bob-pass"); err != nil {
		t.Fatalf("own password %v", err)
	}
	if _, err := users.SetPassword(ctx, admin, "carl", "wrong-password", "new-carl-pass"); !errors.Is(err, ErrCredentials) {
		t.Fatalf("admin without its own current password %v", err)
	}
	if _, err := users.SetPassword(ctx, admin, "carl", "ann-password", "new-carl-pass"); err != nil {
		t.Fatalf("admin changing another's password %v", err)
	}
	if _, err := users.Verify(ctx, "carl", "new-carl-pass"); err != nil {
		t.Fatal("carl's new password", err)
	}
	if err := users.Delete(ctx, operator, "carl"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator removing an account %v", err)
	}
	if err := users.Delete(ctx, admin, "ann"); !errors.Is(err, ErrOwnAccount) {
		t.Fatalf("admin removing itself %v", err)
	}
	if err := users.Delete(ctx, admin, "carl"); err != nil {
		t.Fatal(err)
	}
	tokens := users.tokens
	if _, err := tokens.Create(ctx, operator, "x", []v1.Scope{v1.Scope_SCOPE_ADMIN}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator granting admin %v", err)
	}
	tok, err := tokens.Create(ctx, operator, "x", []v1.Scope{v1.Scope_SCOPE_OPERATE, v1.Scope_SCOPE_INFERENCE})
	if err != nil {
		t.Fatal(err)
	}
	narrow, _ := tokens.Identify(ctx, tok.GetSecret())
	if _, err := tokens.Create(ctx, narrow, "y", []v1.Scope{v1.Scope_SCOPE_READ}); err != nil {
		t.Fatalf("operate opens read %v", err)
	}
	inferenceOnly := narrow
	inferenceOnly.Scopes = []v1.Scope{v1.Scope_SCOPE_INFERENCE}
	if _, err := tokens.Create(ctx, inferenceOnly, "z", []v1.Scope{v1.Scope_SCOPE_READ}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a token granting beyond its own scopes %v", err)
	}
	for _, bad := range [][]v1.Scope{nil, {v1.Scope_SCOPE_UNSPECIFIED}, {v1.Scope(99)}} {
		if _, err := tokens.Create(ctx, operator, "bad", bad); !errors.Is(err, ErrToken) {
			t.Fatalf("scopes %v gave %v", bad, err)
		}
	}
	if _, err := tokens.Create(ctx, daemonID, "d", []v1.Scope{v1.Scope_SCOPE_READ}); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("daemon token holds no tokens %v", err)
	}
	if _, err := tokens.List(ctx, daemonID); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("daemon token lists no tokens %v", err)
	}
	// Removing an account revokes the tokens it made
	if err := users.Delete(ctx, admin, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tokens.Identify(ctx, tok.GetSecret()); ok {
		t.Fatal("a removed account's token still answers")
	}
}

// Concurrent setups through the handler create exactly one admin
func TestSetupRace(t *testing.T) {
	users, sessions := openUsers(t)
	srv, _ := serve(t, Options{Sessions: sessions, Users: users, Log: quiet()})
	const racers = 8
	codes := make(chan int, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			code, _ := post(t, http.DefaultClient, srv.URL+"/auth/local/setup", "application/json", map[string]string{"username": fmt.Sprintf("user%d", i), "password": "racer-password"})
			codes <- code
		})
	}
	wg.Wait()
	close(codes)
	won := 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			won++
		case http.StatusConflict:
		default:
			t.Fatalf("setup answered %d", code)
		}
	}
	list, _ := users.List(context.Background())
	if won != 1 || len(list) != 1 || list[0].GetRole() != v1.Role_ROLE_ADMIN || users.Count() != 1 {
		t.Fatalf("%d setups won, accounts %v", won, list)
	}
}

func TestCertGenerationAndReuse(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	hosts := CertHosts("10.1.2.3:8484", "0.0.0.0:9000", "nebu.test:8485", "", "mesh.test", "[::]:9100")
	for _, h := range []string{"localhost", "127.0.0.1", "::1", "10.1.2.3", "nebu.test", "mesh.test"} {
		if !slices.Contains(hosts, h) {
			t.Fatalf("hosts %v lack %s", hosts, h)
		}
	}
	if slices.Contains(hosts, "0.0.0.0") || slices.Contains(hosts, "::") || slices.Contains(hosts, "") {
		t.Fatalf("hosts %v hold the unspecified address", hosts)
	}
	now := time.Now()
	first, created, err := LoadOrCreateCert(certFile, keyFile, hosts, now)
	if err != nil || !created {
		t.Fatalf("generate %v %v", created, err)
	}
	key, ok := first.Leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		t.Fatalf("key %T", first.Leaf.PublicKey)
	}
	for _, h := range hosts {
		if err := first.Leaf.VerifyHostname(h); err != nil {
			t.Fatalf("certificate does not name %s: %v", h, err)
		}
	}
	if info, err := os.Stat(keyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v %v", info.Mode(), err)
	}
	fp := Fingerprint(first)
	if len(fp) != 32*3-1 || strings.Count(fp, ":") != 31 {
		t.Fatalf("fingerprint %q", fp)
	}
	// The next start reuses the pair
	again, created, err := LoadOrCreateCert(certFile, keyFile, hosts, now.Add(time.Hour))
	if err != nil || created || Fingerprint(again) != fp {
		t.Fatalf("reuse %v %v", created, err)
	}
	// A TLS client trusting the certificate completes a handshake against it
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{again}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(again.Leaf)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// A host the pair does not name, or an expiry inside the renewal window, makes a new pair
	wider, created, err := LoadOrCreateCert(certFile, keyFile, append(hosts, "other.test"), now)
	if err != nil || !created || Fingerprint(wider) == fp || wider.Leaf.VerifyHostname("other.test") != nil {
		t.Fatalf("new host %v %v", created, err)
	}
	if _, created, err := LoadOrCreateCert(certFile, keyFile, hosts, now.Add(certLifetime-certRenewBefore/2)); err != nil || !created {
		t.Fatalf("near expiry %v %v", created, err)
	}
	os.WriteFile(certFile, []byte("garbage"), 0o644)
	if _, _, err := LoadOrCreateCert(certFile, keyFile, hosts, now); err == nil {
		t.Fatal("an unreadable pair should fail")
	}
}

func TestRedactEnv(t *testing.T) {
	env := map[string]string{"HF_TOKEN": "hf_abc123", "LONGER": "hf_abc123456", "DEV": "0", "EMPTY": ""}
	words := []string{"vllm", "--port", "8080", "--token=hf_abc123", "Bearer hf_abc123456", "--devices", "0", "cuda:0,1", "https://u:hf_abc123@host/x"}
	want := []string{"vllm", "--port", "8080", "--token=" + Redacted, "Bearer " + Redacted, "--devices", Redacted, "cuda:" + Redacted + ",1", "https://u:" + Redacted + "@host/x"}
	got := RedactEnv(words, env)
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if words[3] != "--token=hf_abc123" {
		t.Fatal("the input was changed")
	}
	if got := RedactEnv([]string{"a", "b"}, nil); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("no env %q", got)
	}
}

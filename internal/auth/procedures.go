package auth

import (
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
)

// What one procedure takes, no credential, a member session, or a credential holding the scope
type rule struct {
	scope v1.Scope
	// A node outside any mesh calls it without a credential, its join grant and admission deciding
	open bool
	// A member's session token opens it beside a credential holding the scope
	peer bool
}

var (
	needInference = rule{scope: v1.Scope_SCOPE_INFERENCE}
	needRead      = rule{scope: v1.Scope_SCOPE_READ}
	needOperate   = rule{scope: v1.Scope_SCOPE_OPERATE}
	needAdmin     = rule{scope: v1.Scope_SCOPE_ADMIN}
	openToAll     = rule{open: true}
	// Node to node calls, which a person reaches with the admin scope
	peerOrAdmin = rule{scope: v1.Scope_SCOPE_ADMIN, peer: true}
	// Tool calls members forward, which a person reaches with the inference scope
	peerOrInference = rule{scope: v1.Scope_SCOPE_INFERENCE, peer: true}
)

// Every procedure the daemon serves with the rule that opens it, any other refused
var procedures = map[string]rule{
	nebuv1connect.AuthServiceListUsersProcedure:   needAdmin,
	nebuv1connect.AuthServiceCreateUserProcedure:  needAdmin,
	nebuv1connect.AuthServiceDeleteUserProcedure:  needAdmin,
	nebuv1connect.AuthServiceSetPasswordProcedure: needInference,
	nebuv1connect.AuthServiceListTokensProcedure:  needInference,
	nebuv1connect.AuthServiceCreateTokenProcedure: needInference,
	nebuv1connect.AuthServiceDeleteTokenProcedure: needInference,

	nebuv1connect.BotServiceListBotsProcedure:        needRead,
	nebuv1connect.BotServiceGetBotProcedure:          needRead,
	nebuv1connect.BotServiceListBotActivityProcedure: needRead,
	nebuv1connect.BotServiceListBotGuildsProcedure:   needRead,
	nebuv1connect.BotServiceCreateBotProcedure:       needOperate,
	nebuv1connect.BotServiceUpdateBotProcedure:       needOperate,
	nebuv1connect.BotServiceDeleteBotProcedure:       needOperate,
	nebuv1connect.BotServiceStartBotProcedure:        needOperate,
	nebuv1connect.BotServiceStopBotProcedure:         needOperate,
	nebuv1connect.BotServiceProbeBotTokenProcedure:   needOperate,
	nebuv1connect.BotServiceSendBotMessageProcedure:  needOperate,

	nebuv1connect.BuildServiceListRecipesProcedure: needRead,
	nebuv1connect.BuildServiceListBuildsProcedure:  needRead,
	nebuv1connect.BuildServiceGetBuildProcedure:    needRead,
	nebuv1connect.BuildServiceBuildProcedure:       needOperate,
	nebuv1connect.BuildServiceRemoveBuildProcedure: needOperate,

	nebuv1connect.ChatServiceListConversationsProcedure:  needInference,
	nebuv1connect.ChatServiceGetConversationProcedure:    needInference,
	nebuv1connect.ChatServicePutConversationProcedure:    needInference,
	nebuv1connect.ChatServiceRenameConversationProcedure: needInference,
	nebuv1connect.ChatServiceDeleteConversationProcedure: needInference,
	nebuv1connect.ChatServicePutChatFileProcedure:        needInference,
	nebuv1connect.ChatServiceGetChatFileProcedure:        needInference,
	nebuv1connect.ChatServiceDeleteChatFilesProcedure:    needInference,

	nebuv1connect.EstimateServiceInspectProcedure:  needRead,
	nebuv1connect.EstimateServiceEstimateProcedure: needRead,
	nebuv1connect.EstimateServicePartsProcedure:    needRead,

	nebuv1connect.EventServiceWatchEventsProcedure: needRead,

	nebuv1connect.GatewayServiceGetGatewayStatusProcedure:      needRead,
	nebuv1connect.GatewayServiceListRoutesProcedure:            needRead,
	nebuv1connect.GatewayServiceGetRouteProcedure:              needRead,
	nebuv1connect.GatewayServiceListPoolsProcedure:             needRead,
	nebuv1connect.GatewayServiceGetPoolProcedure:               needRead,
	nebuv1connect.GatewayServiceListObjectivesProcedure:        needRead,
	nebuv1connect.GatewayServiceListSchedulerConfigsProcedure:  needRead,
	nebuv1connect.GatewayServiceListTracesProcedure:            needRead,
	nebuv1connect.GatewayServiceGetTraceProcedure:              needRead,
	nebuv1connect.GatewayServicePutRouteProcedure:              needOperate,
	nebuv1connect.GatewayServiceDeleteRouteProcedure:           needOperate,
	nebuv1connect.GatewayServicePutPoolProcedure:               needOperate,
	nebuv1connect.GatewayServiceDeletePoolProcedure:            needOperate,
	nebuv1connect.GatewayServicePutObjectiveProcedure:          needOperate,
	nebuv1connect.GatewayServiceDeleteObjectiveProcedure:       needOperate,
	nebuv1connect.GatewayServicePutSchedulerConfigProcedure:    needOperate,
	nebuv1connect.GatewayServiceDeleteSchedulerConfigProcedure: needOperate,

	nebuv1connect.HostServiceGetProfileProcedure:    needRead,
	nebuv1connect.HostServiceDoctorProcedure:        needRead,
	nebuv1connect.HostServiceLogsProcedure:          needRead,
	nebuv1connect.HostServiceListDirectoryProcedure: needOperate,

	nebuv1connect.InstanceServiceListInstancesProcedure: needRead,
	nebuv1connect.InstanceServiceGetInstanceProcedure:   needRead,
	nebuv1connect.InstanceServiceLogsProcedure:          needRead,
	nebuv1connect.InstanceServicePreviewRunProcedure:    needRead,
	nebuv1connect.InstanceServiceRunProcedure:           needOperate,
	nebuv1connect.InstanceServiceStopInstanceProcedure:  needOperate,
	nebuv1connect.InstanceServiceLoadAdapterProcedure:   needOperate,
	nebuv1connect.InstanceServiceUnloadAdapterProcedure: needOperate,
	nebuv1connect.InstanceServiceListAdaptersProcedure:  needRead,

	nebuv1connect.MeshServiceInitProcedure:                needAdmin,
	nebuv1connect.MeshServiceJoinProcedure:                needAdmin,
	nebuv1connect.MeshServiceLeaveProcedure:               needAdmin,
	nebuv1connect.MeshServiceResetMeshProcedure:           needAdmin,
	nebuv1connect.MeshServiceRotateProcedure:              needAdmin,
	nebuv1connect.MeshServiceTokenProcedure:               needAdmin,
	nebuv1connect.MeshServiceInviteProcedure:              needAdmin,
	nebuv1connect.MeshServiceAskProcedure:                 needAdmin,
	nebuv1connect.MeshServiceAdmitProcedure:               needAdmin,
	nebuv1connect.MeshServiceRefuseProcedure:              needAdmin,
	nebuv1connect.MeshServiceHandOffIssuerProcedure:       needAdmin,
	nebuv1connect.MeshServiceDismissProcedure:             needAdmin,
	nebuv1connect.MeshServiceExportTokenProcedure:         needAdmin,
	nebuv1connect.MeshServiceImportMeshProcedure:          needAdmin,
	nebuv1connect.MeshServiceDropFederationProcedure:      needAdmin,
	nebuv1connect.MeshServiceGetMeshProcedure:             needRead,
	nebuv1connect.MeshServiceListNodesProcedure:           needRead,
	nebuv1connect.MeshServiceDiscoverProcedure:            needRead,
	nebuv1connect.MeshServicePlanFormationProcedure:       needRead,
	nebuv1connect.MeshServiceListFormationsProcedure:      needRead,
	nebuv1connect.MeshServiceGetFormationProcedure:        needRead,
	nebuv1connect.MeshServiceFormationLogsProcedure:       needRead,
	nebuv1connect.MeshServiceProbeProcedure:               needOperate,
	nebuv1connect.MeshServiceSetDeviceProfileProcedure:    needOperate,
	nebuv1connect.MeshServiceDeleteDeviceProfileProcedure: needOperate,
	nebuv1connect.MeshServiceRunFormationProcedure:        needOperate,
	nebuv1connect.MeshServiceStopFormationProcedure:       needOperate,
	nebuv1connect.MeshServiceDeleteFormationProcedure:     needOperate,
	nebuv1connect.MeshServiceForgetNodeProcedure:          peerOrAdmin,
	nebuv1connect.MeshServiceRunSeatProcedure:             peerOrAdmin,
	nebuv1connect.MeshServiceStopSeatProcedure:            peerOrAdmin,
	nebuv1connect.MeshServiceMoveSlotProcedure:            peerOrAdmin,
	nebuv1connect.MeshServiceDropSlotProcedure:            peerOrAdmin,
	nebuv1connect.MeshServiceGetSeatProcedure:             peerOrAdmin,
	nebuv1connect.MeshServicePullSeatProcedure:            peerOrAdmin,
	nebuv1connect.MeshServiceWatchPullProcedure:           peerOrAdmin,
	nebuv1connect.MeshServiceSeatLogsProcedure:            peerOrAdmin,
	nebuv1connect.MeshServiceGetStoredProcedure:           peerOrAdmin,

	nebuv1connect.MeshPeerServiceKnockProcedure:      openToAll,
	nebuv1connect.MeshPeerServiceOfferProcedure:      openToAll,
	nebuv1connect.MeshPeerServiceWelcomeProcedure:    openToAll,
	nebuv1connect.MeshPeerServiceEnrollProcedure:     openToAll,
	nebuv1connect.MeshPeerServiceFederateProcedure:   openToAll,
	nebuv1connect.MeshPeerServiceExportsProcedure:    openToAll,
	nebuv1connect.MeshPeerServiceRenewProcedure:      peerOrAdmin,
	nebuv1connect.MeshPeerServiceGrantProcedure:      peerOrAdmin,
	nebuv1connect.MeshPeerServiceReissueProcedure:    peerOrAdmin,
	nebuv1connect.MeshPeerServiceTakeIssuerProcedure: peerOrAdmin,
	nebuv1connect.MeshPeerServiceSyncProcedure:       peerOrAdmin,
	nebuv1connect.MeshPeerServiceByeProcedure:        peerOrAdmin,
	nebuv1connect.MeshPeerServiceStreamProcedure:     peerOrAdmin,

	nebuv1connect.RuntimeServiceListFormatsProcedure:   needRead,
	nebuv1connect.RuntimeServiceListRuntimesProcedure:  needRead,
	nebuv1connect.RuntimeServiceGetRuntimeProcedure:    needRead,
	nebuv1connect.RuntimeServiceListInstallsProcedure:  needRead,
	nebuv1connect.RuntimeServiceAdoptInstallProcedure:  needOperate,
	nebuv1connect.RuntimeServiceInstallProcedure:       needOperate,
	nebuv1connect.RuntimeServiceRemoveInstallProcedure: needOperate,

	nebuv1connect.SettingsServiceGetSettingsProcedure:    needAdmin,
	nebuv1connect.SettingsServiceUpdateSettingsProcedure: needAdmin,

	nebuv1connect.SlotServiceListSlotsProcedure:    needRead,
	nebuv1connect.SlotServiceGetSlotProcedure:      needRead,
	nebuv1connect.SlotServiceCreateSlotProcedure:   needOperate,
	nebuv1connect.SlotServiceUpdateSlotProcedure:   needOperate,
	nebuv1connect.SlotServiceDeleteSlotProcedure:   needOperate,
	nebuv1connect.SlotServiceSwapProcedure:         needOperate,
	nebuv1connect.SlotServiceEvictSlotProcedure:    needOperate,
	nebuv1connect.SlotServiceRelaunchSlotProcedure: needOperate,

	nebuv1connect.SourceServiceListSourcesProcedure:   needRead,
	nebuv1connect.SourceServiceListProvidersProcedure: needRead,
	nebuv1connect.SourceServiceSearchProcedure:        needRead,
	nebuv1connect.SourceServiceResolveProcedure:       needRead,
	nebuv1connect.SourceServiceListRevisionsProcedure: needRead,
	nebuv1connect.SourceServiceGetModelCardProcedure:  needRead,
	nebuv1connect.SourceServiceCreateSourceProcedure:  needAdmin,
	nebuv1connect.SourceServiceUpdateSourceProcedure:  needAdmin,
	nebuv1connect.SourceServiceDeleteSourceProcedure:  needAdmin,

	nebuv1connect.StoreServiceListModelsProcedure:  needRead,
	nebuv1connect.StoreServiceGetModelProcedure:    needRead,
	nebuv1connect.StoreServiceGetStatusProcedure:   needRead,
	nebuv1connect.StoreServicePullProcedure:        needOperate,
	nebuv1connect.StoreServiceRemoveModelProcedure: needOperate,
	nebuv1connect.StoreServiceGcProcedure:          needOperate,
	nebuv1connect.StoreServiceVerifyProcedure:      needOperate,
	nebuv1connect.StoreServiceExportProcedure:      needOperate,

	nebuv1connect.TaskServiceListTasksProcedure:  needRead,
	nebuv1connect.TaskServiceGetTaskProcedure:    needRead,
	nebuv1connect.TaskServiceWatchTaskProcedure:  needRead,
	nebuv1connect.TaskServiceCancelTaskProcedure: needOperate,

	nebuv1connect.ToolServiceListToolServersProcedure: peerOrInference,
	nebuv1connect.ToolServiceCallToolProcedure:        peerOrInference,

	nebuv1connect.PoolImportServiceListPoolImportsProcedure:  needRead,
	nebuv1connect.PoolImportServiceGetPoolImportProcedure:    needRead,
	nebuv1connect.PoolImportServiceWatchPoolImportsProcedure: needRead,
}

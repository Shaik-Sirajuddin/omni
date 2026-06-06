package server

import "github.com/Shaik-Sirajuddin/memory/mcp/server/service"

type targetSpec = service.TargetSpec
type payloadMessage = service.PayloadMessage
type sendMessageRequest = service.SendMessageRequest
type sendGroupMessageRequest = service.SendGroupMessageRequest
type sendMessageResponse = service.SendMessageResponse
type sendGroupMessageResponse = service.SendGroupMessageResponse
type errorResponse = service.ErrorResponse
type healthResponse = service.HealthResponse
type senderSpec = service.SenderSpec
type listMessagesRequest = service.ListMessagesRequest
type listRequest = service.ListRequest
type deleteMessageResponse = service.DeleteMessageResponse
type agentControlRequest = service.AgentControlRequest
type agentStatusResponse = service.AgentStatusResponse
type listTeamsResponse = service.ListTeamsResponse

package spamoor

import (
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/sirupsen/logrus"
)

// loggingRawTracer implements pubsub.RawTracer and logs every event the
// gossipsub router emits. Useful for debugging mesh formation, peer churn,
// and message validation outcomes against live consensus peers.
type loggingRawTracer struct {
	log logrus.FieldLogger
}

func newLoggingRawTracer(log logrus.FieldLogger) *loggingRawTracer {
	return &loggingRawTracer{log: log.WithField("subcomponent", "gossipsub-tracer")}
}

func (t *loggingRawTracer) AddPeer(p peer.ID, proto protocol.ID) {
	t.log.WithFields(logrus.Fields{"peer": p.String(), "proto": string(proto)}).Debug("BHARATH:AddPeer")
}

func (t *loggingRawTracer) RemovePeer(p peer.ID) {
	t.log.WithField("peer", p.String()).Debug("BHARATH:RemovePeer")
}

func (t *loggingRawTracer) Join(topic string) {
	t.log.WithField("topic", topic).Debug("BHARATH:Join")
}

func (t *loggingRawTracer) Leave(topic string) {
	t.log.WithField("topic", topic).Debug("BHARATH:Leave")
}

func (t *loggingRawTracer) Graft(p peer.ID, topic string) {
	t.log.WithFields(logrus.Fields{"peer": p.String(), "topic": topic}).Debug("BHARATH:Graft")
}

func (t *loggingRawTracer) Prune(p peer.ID, topic string) {
	t.log.WithFields(logrus.Fields{"peer": p.String(), "topic": topic}).Debug("BHARATH:Prune")
}

func (t *loggingRawTracer) ValidateMessage(msg *pubsub.Message) {
	t.log.WithFields(messageFields(msg)).Debug("BHARATH:ValidateMessage")
}

func (t *loggingRawTracer) DeliverMessage(msg *pubsub.Message) {
	t.log.WithFields(messageFields(msg)).Debug("BHARATH:DeliverMessage")
}

func (t *loggingRawTracer) RejectMessage(msg *pubsub.Message, reason string) {
	fields := messageFields(msg)
	fields["reason"] = reason
	t.log.WithFields(fields).Debug("BHARATH:RejectMessage")
}

func (t *loggingRawTracer) DuplicateMessage(msg *pubsub.Message) {
	t.log.WithFields(messageFields(msg)).Debug("BHARATH:DuplicateMessage")
}

func (t *loggingRawTracer) ThrottlePeer(p peer.ID) {
	t.log.WithField("peer", p.String()).Debug("BHARATH:ThrottlePeer")
}

func (t *loggingRawTracer) RecvRPC(rpc *pubsub.RPC) {
	t.log.WithFields(rpcFields(rpc)).Debug("BHARATH:RecvRPC")
}

func (t *loggingRawTracer) SendRPC(rpc *pubsub.RPC, p peer.ID) {
	fields := rpcFields(rpc)
	fields["peer"] = p.String()
	t.log.WithFields(fields).Debug("BHARATH:SendRPC")
}

func (t *loggingRawTracer) DropRPC(rpc *pubsub.RPC, p peer.ID) {
	fields := rpcFields(rpc)
	fields["peer"] = p.String()
	t.log.WithFields(fields).Debug("BHARATH:DropRPC")
}

func (t *loggingRawTracer) UndeliverableMessage(msg *pubsub.Message) {
	t.log.WithFields(messageFields(msg)).Debug("BHARATH:UndeliverableMessage")
}

func messageFields(msg *pubsub.Message) logrus.Fields {
	fields := logrus.Fields{
		"from":  msg.GetFrom().String(),
		"recv":  msg.ReceivedFrom.String(),
		"local": msg.Local,
		"size":  len(msg.GetData()),
	}
	if topic := msg.GetTopic(); topic != "" {
		fields["topic"] = topic
	}
	if msg.ID != "" {
		fields["msg_id_len"] = len(msg.ID)
	}
	return fields
}

func rpcFields(rpc *pubsub.RPC) logrus.Fields {
	fields := logrus.Fields{
		"subs":     len(rpc.GetSubscriptions()),
		"messages": len(rpc.GetPublish()),
	}
	if ctrl := rpc.GetControl(); ctrl != nil {
		fields["ctrl_graft"] = len(ctrl.GetGraft())
		fields["ctrl_prune"] = len(ctrl.GetPrune())
		fields["ctrl_ihave"] = len(ctrl.GetIhave())
		fields["ctrl_iwant"] = len(ctrl.GetIwant())
	}
	return fields
}

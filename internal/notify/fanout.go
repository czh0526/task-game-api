package notify

// Fanout is the browser signal fan-out consumed by the SSE handler.
//
// *Broker is the single-process implementation: it keeps subscribers in this
// process only. *RemoteFanout wraps a Broker and additionally publishes every
// signal over a Transport so other API processes can deliver it to their own
// local subscribers.
//
// This interface exists so svc.ServiceContext can swap the implementation
// without touching the handler, the engine, or the frontend.
type Fanout interface {
	Subscribe(userID string) (<-chan Signal, func())
	Publish(userIDs []string, s Signal)
	NextID() uint64
	Subscribers(userID string) int
}

var _ Fanout = (*Broker)(nil)
var _ Fanout = (*RemoteFanout)(nil)

package workflows

// Signal names.
const (
	SignalApprove  = "approve"         // operator → target
	SignalReject   = "reject"          // operator → target
	SignalResume   = "resume"          // operator → target (paused only)
	SignalRetry    = "retry"           // operator → target (paused only)
	SignalAbort    = "abort"           // operator → target (paused only) or global
	SignalStop     = "stop-after-step" // stage → target
	SignalProgress = "target-progress" // target → global
)

// OperatorSignal is the payload of operator signals. Validation scopes
// approve/reject to one awaiting validation; empty means all.
type OperatorSignal struct {
	Validation string `json:"validation,omitempty"`
}

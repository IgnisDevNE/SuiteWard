package installer

// Stack is the three containers of one instance, in start order.
type Stack struct{ DB, Migrate, Serve RunSpec }

// ColocatedStack is the co-located stack: the database, the one-shot migrate and the service, with the flag sets of
// the quadlets. image is the release image (pinned by digest for an install).
func ColocatedStack(s Settings, image string) (Stack, error) { return Stack{}, errNotImplemented }

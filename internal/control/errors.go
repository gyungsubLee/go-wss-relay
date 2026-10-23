package control

// Error describes a business failure independently of its HTTP representation.
// Kind is invalid, forbidden, not_found, conflict, gone or unavailable.
type Error struct {
	Kind    string
	Message string
}

func (err *Error) Error() string { return err.Message }

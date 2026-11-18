package adminsdk

// The batch status enum shares names with the error enum. Keep the original
// exported error constants available alongside the generator's qualified names.
const (
	Conflict        = AdminErrorCodeConflict
	Forbidden       = AdminErrorCodeForbidden
	InvalidRequest  = AdminErrorCodeInvalidRequest
	NotFound        = AdminErrorCodeNotFound
	Unauthenticated = AdminErrorCodeUnauthenticated
	Unavailable     = AdminErrorCodeUnavailable
)

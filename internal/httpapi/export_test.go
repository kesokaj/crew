package httpapi

// For the tests of the route lists (sessions_test.go): the lists, and the patterns the API registers.
var (
	FreshRoutes = freshRoutes
	PlainRoutes = plainRoutes
	FreshKeys   = freshKeys
)

func (s *Server) Patterns() []string {
	_, p := s.apiRoutes()
	return p
}

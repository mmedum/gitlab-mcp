package render

// FixedBoundary is a boundary with a known token, for goldens.
func FixedBoundary(token string) Boundary { return Boundary{token: token} }

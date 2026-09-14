package httpapi

// @title wzap
// @version 1.0
// @description Standalone multi-instance WhatsApp gateway. Authenticated REST commands plus durable events; every success body is wrapped in a {"data": ...} envelope and every failure in a {"error": {"code", "message"}} envelope.
// @BasePath /
// @securityDefinitions.apikey apikey
// @in header
// @name apikey
// @description Instance or global API key sent in the literal apikey header. Session-cookie holders use /auth/* instead.

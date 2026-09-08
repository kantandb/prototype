package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type api struct {
	store *store
}

func newHandler(store *store) http.Handler {
	a := &api{store: store}

	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", a.health)

	return router
}

func (a *api) health(c *gin.Context) {
	_ = a.store.db.Metrics()

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

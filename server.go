package main

import (
	"net/http"

	"github.com/cockroachdb/pebble"
	"github.com/gin-gonic/gin"
)

type api struct {
	db *pebble.DB
}

func newHandler(db *pebble.DB) http.Handler {
	a := &api{db: db}

	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", a.health)

	return router
}

func (a *api) health(c *gin.Context) {
	_ = a.db.Metrics()

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

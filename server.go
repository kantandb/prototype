package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
)

type api struct {
	store        *store
	maxBodyBytes int64
}

type dbRequest struct {
	Name string `json:"name"`
}

type dbList struct {
	Databases []string `json:"databases"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newHandler(store *store, maxBodyBytes int64) http.Handler {
	a := &api{store: store, maxBodyBytes: maxBodyBytes}

	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", a.health)
	router.POST("/", a.createDB)
	router.GET("/", a.listDBs)
	router.DELETE("/:database", a.deleteDB)

	return router
}

func (a *api) health(c *gin.Context) {
	_ = a.store.db.Metrics()

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (a *api) createDB(c *gin.Context) {
	if !isJSON(c.GetHeader("Content-Type")) {
		writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")

		return
	}

	body, err := readBody(c.Request.Body, a.maxBodyBytes)
	if errors.Is(err, errBodyTooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "content_too_large", "Request body exceeds the size limit")

		return
	}
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "Could not read request body")

		return
	}

	request, err := decodeDBRequest(body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "Request body must contain only a database name")

		return
	}
	if err := validateName(request.Name); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_name", "Database name is invalid")

		return
	}

	if err := a.store.createDB(request.Name); errors.Is(err, errDBExists) {
		writeError(c, http.StatusConflict, "database_exists", "Database already exists")

		return
	} else if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not create database")

		return
	}

	c.Header("Location", "/"+request.Name)
	c.JSON(http.StatusCreated, request)
}

func (a *api) listDBs(c *gin.Context) {
	names, err := a.store.listDBs()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not list databases")

		return
	}
	if names == nil {
		names = []string{}
	}

	c.JSON(http.StatusOK, dbList{Databases: names})
}

func (a *api) deleteDB(c *gin.Context) {
	name := c.Param("database")
	if err := validateName(name); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_name", "Database name is invalid")

		return
	}

	if err := a.store.deleteDB(name); errors.Is(err, errDBNotFound) {
		writeError(c, http.StatusNotFound, "database_not_found", "Database does not exist")

		return
	} else if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not delete database")

		return
	}

	c.Status(http.StatusNoContent)
}

func decodeDBRequest(body []byte) (dbRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	var request dbRequest
	if err := decoder.Decode(&request); err != nil {
		return dbRequest{}, err
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return dbRequest{}, errors.New("multiple JSON values")
		}

		return dbRequest{}, err
	}

	return request, nil
}

func isJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)

	return err == nil && mediaType == "application/json"
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

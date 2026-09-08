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

type docResponse struct {
	ID string `json:"id"`
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
	router.POST("/:database/", a.createDoc)
	router.GET("/:database/:id", a.getDoc)
	router.PUT("/:database/:id", a.replaceDoc)
	router.DELETE("/:database/:id", a.deleteDoc)

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

func (a *api) createDoc(c *gin.Context) {
	if !isJSON(c.GetHeader("Content-Type")) {
		writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")

		return
	}

	database := c.Param("database")
	if err := validateName(database); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_name", "Database name is invalid")

		return
	}

	body, err := readBody(c.Request.Body, a.maxBodyBytes)
	if errors.Is(err, errBodyTooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "content_too_large", "Request body exceeds the size limit")

		return
	}
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_document", "Could not read document")

		return
	}

	document, err := validateDoc(body, a.maxBodyBytes)
	if errors.Is(err, errBodyTooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "content_too_large", "Document exceeds the size limit")

		return
	}
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_document", "Document must be a JSON object")

		return
	}

	id, err := makeID()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not create document")

		return
	}
	rev, err := a.store.createDoc(database, id, document)
	if errors.Is(err, errDBNotFound) {
		writeError(c, http.StatusNotFound, "database_not_found", "Database does not exist")

		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not create document")

		return
	}

	c.Header("Location", "/"+database+"/"+id)
	c.Header("ETag", formatETag(rev))
	c.JSON(http.StatusCreated, docResponse{ID: id})
}

func (a *api) getDoc(c *gin.Context) {
	if !validDocPath(c) {
		return
	}

	doc, err := a.store.getDoc(c.Param("database"), c.Param("id"))
	if errors.Is(err, errDocNotFound) {
		writeError(c, http.StatusNotFound, "document_not_found", "Document does not exist")

		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not read document")

		return
	}

	c.Header("ETag", formatETag(doc.revision))
	c.Data(http.StatusOK, "application/json", doc.json)
}

func (a *api) replaceDoc(c *gin.Context) {
	if !isJSON(c.GetHeader("Content-Type")) {
		writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")

		return
	}
	if !validDocPath(c) {
		return
	}

	match, err := parseIfMatch(c.Request.Header.Values("If-Match"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_if_match", "If-Match is invalid")

		return
	}

	body, err := readBody(c.Request.Body, a.maxBodyBytes)
	if errors.Is(err, errBodyTooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "content_too_large", "Request body exceeds the size limit")

		return
	}
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_document", "Could not read document")

		return
	}

	document, err := validateDoc(body, a.maxBodyBytes)
	if errors.Is(err, errBodyTooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "content_too_large", "Document exceeds the size limit")

		return
	}
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_document", "Document must be a JSON object")

		return
	}

	rev, err := a.store.replaceDoc(c.Param("database"), c.Param("id"), document, match)
	if errors.Is(err, errDocNotFound) {
		writeError(c, http.StatusNotFound, "document_not_found", "Document does not exist")

		return
	}
	if errors.Is(err, errPreconditionFailed) {
		writeError(c, http.StatusPreconditionFailed, "precondition_failed", "If-Match precondition failed")

		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not replace document")

		return
	}

	c.Header("ETag", formatETag(rev))
	c.Data(http.StatusOK, "application/json", document)
}

func (a *api) deleteDoc(c *gin.Context) {
	if !validDocPath(c) {
		return
	}

	match, err := parseIfMatch(c.Request.Header.Values("If-Match"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_if_match", "If-Match is invalid")

		return
	}

	err = a.store.deleteDoc(c.Param("database"), c.Param("id"), match)
	if errors.Is(err, errDocNotFound) {
		writeError(c, http.StatusNotFound, "document_not_found", "Document does not exist")

		return
	}
	if errors.Is(err, errPreconditionFailed) {
		writeError(c, http.StatusPreconditionFailed, "precondition_failed", "If-Match precondition failed")

		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal_error", "Could not delete document")

		return
	}

	c.Status(http.StatusNoContent)
}

func validDocPath(c *gin.Context) bool {
	if err := validateName(c.Param("database")); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_name", "Database name is invalid")

		return false
	}
	if err := validateID(c.Param("id")); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_id", "Document ID is invalid")

		return false
	}

	return true
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

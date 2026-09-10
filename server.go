package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

type api struct {
	store        *store
	maxBodyBytes int64
	log          *slog.Logger
	stopping     atomic.Bool
}

type dbRequest struct {
	Name    string          `json:"name"`
	Indexes *[]indexRequest `json:"indexes,omitempty"`
}

type indexRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (r dbRequest) indexDefs() []indexDef {
	if r.Indexes == nil {
		return nil
	}

	defs := make([]indexDef, len(*r.Indexes))
	for i, index := range *r.Indexes {
		defs[i] = indexDef{name: index.Name, path: index.Path}
	}

	return defs
}

type dbList struct {
	Databases []string `json:"databases"`
}

type docList struct {
	Documents []string `json:"documents"`
	Cursor    string   `json:"cursor"`
}

type docQuery struct {
	value   any
	cursor  string
	index   string
	limit   int
	indexed bool
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
	return newAPI(store, maxBodyBytes, slog.New(slog.DiscardHandler)).handler()
}

func newAPI(store *store, maxBodyBytes int64, log *slog.Logger) *api {
	return &api{store: store, maxBodyBytes: maxBodyBytes, log: log}
}

const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

func (a *api) handler() http.Handler {
	router := gin.New()
	router.HandleMethodNotAllowed = true
	router.RedirectFixedPath = false
	router.RedirectTrailingSlash = false
	router.Use(a.recover, a.rejectStopping)
	router.GET("/healthz", a.health)
	router.POST("/", a.createDB)
	router.GET("/", a.listDBs)
	router.GET("/:database", a.listDocs)
	router.DELETE("/:database", a.deleteDB)
	router.POST("/:database/", a.createDoc)
	router.GET("/:database/:id", a.getDoc)
	router.PUT("/:database/:id", a.replaceDoc)
	router.PATCH("/:database/:id", a.patchDoc)
	router.DELETE("/:database/:id", a.deleteDoc)
	router.NoRoute(func(c *gin.Context) {
		writeError(c, http.StatusNotFound, "route_not_found", "Route does not exist")
	})
	router.NoMethod(func(c *gin.Context) {
		writeError(c, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed")
	})

	return router
}

func (a *api) stop() {
	a.stopping.Store(true)
}

func (a *api) recover(c *gin.Context) {
	defer func() {
		value := recover()
		if value == nil {
			return
		}

		err, ok := value.(error)
		if !ok {
			err = fmt.Errorf("panic: %v", value)
		}
		a.log.Error("request panic", "method", c.Request.Method, "path", c.Request.URL.Path, "error", err, "stack", string(debug.Stack()))
		if !c.Writer.Written() {
			writeFailure(c, err)
		}
		c.Abort()
	}()

	c.Next()
}

func (a *api) rejectStopping(c *gin.Context) {
	if a.stopping.Load() {
		writeError(c, http.StatusServiceUnavailable, "service_unavailable", "Service is unavailable")
		c.Abort()

		return
	}

	c.Next()
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
		writeError(c, http.StatusBadRequest, "invalid_request", "Request body must contain a database name and optional indexes")

		return
	}
	if err := validateName(request.Name); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_name", "Database name is invalid")

		return
	}

	err = a.store.createDB(request.Name, request.indexDefs()...)
	if errors.Is(err, errDBExists) {
		writeError(c, http.StatusConflict, "database_exists", "Database already exists")

		return
	}
	if errors.Is(err, errInvalidIndex) {
		writeError(c, http.StatusBadRequest, "invalid_request", "Index definitions are invalid")

		return
	}
	if err != nil {
		a.fail(c, "create database", err)

		return
	}

	c.Header("Location", "/"+request.Name)
	c.JSON(http.StatusCreated, request)
}

func (a *api) listDBs(c *gin.Context) {
	limit, cursor, ok := parseListQuery(c, validateName)
	if !ok {
		return
	}

	names, err := a.store.listDBs(limit, cursor)
	if err != nil {
		a.fail(c, "list databases", err)

		return
	}
	if names == nil {
		names = []string{}
	}

	c.JSON(http.StatusOK, dbList{Databases: names})
}

func (a *api) listDocs(c *gin.Context) {
	database := c.Param("database")
	if err := validateName(database); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_name", "Database name is invalid")

		return
	}

	query, ok := parseDocQuery(c)
	if !ok {
		return
	}

	var ids []string
	var encoded []byte
	var more bool
	var err error
	if query.indexed {
		var cursor string
		encoded, cursor, err = queryStart(database, query)
		if errors.Is(err, errInvalidQueryCursor) {
			writeError(c, http.StatusBadRequest, "invalid_cursor", "Cursor is invalid")

			return
		}
		if err == nil {
			ids, more, err = a.store.queryDocs(database, query.index, encoded, query.limit, cursor)
		}
	} else {
		ids, more, err = a.store.listDocs(database, query.limit, query.cursor)
	}
	if errors.Is(err, errDBNotFound) {
		writeError(c, http.StatusNotFound, "database_not_found", "Database does not exist")

		return
	}
	if errors.Is(err, errIndexNotFound) {
		writeError(c, http.StatusNotFound, "index_not_found", "Index does not exist")

		return
	}
	if errors.Is(err, errInvalidIndexValue) {
		writeError(c, http.StatusBadRequest, "invalid_query", "Query is invalid")

		return
	}
	if err != nil {
		a.fail(c, "list documents", err)

		return
	}

	if ids == nil {
		ids = []string{}
	}

	cursor := ""
	if more && query.indexed {
		cursor, err = encodeQueryCursor(queryCursor{database: database, index: query.index, value: encoded, id: ids[len(ids)-1]})
		if err != nil {
			a.fail(c, "encode query cursor", err)

			return
		}
	} else if more {
		cursor = ids[len(ids)-1]
	}

	c.JSON(http.StatusOK, docList{Documents: ids, Cursor: cursor})
}

func queryStart(database string, query docQuery) ([]byte, string, error) {
	encoded, err := encodeIndexValue(query.value)
	if err != nil {
		return nil, "", err
	}
	if query.cursor == "" {
		return encoded, "", nil
	}

	cursor, err := decodeQueryCursor(query.cursor)
	if err != nil || cursor.database != database || cursor.index != query.index || !bytes.Equal(cursor.value, encoded) {
		return nil, "", errInvalidQueryCursor
	}

	return encoded, cursor.id, nil
}

func parseDocQuery(c *gin.Context) (docQuery, bool) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_query", "Query is invalid")

		return docQuery{}, false
	}
	for name, entries := range values {
		if name != "limit" && name != "cursor" && name != "index" && name != "value" || len(entries) != 1 {
			writeError(c, http.StatusBadRequest, "invalid_query", "Query is invalid")

			return docQuery{}, false
		}
	}

	query := docQuery{limit: defaultListLimit}
	if entries, ok := values["limit"]; ok {
		limit, err := strconv.Atoi(entries[0])
		if err != nil || limit < 1 || limit > maxListLimit {
			writeError(c, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 1000")

			return docQuery{}, false
		}
		query.limit = limit
	}
	if entries, ok := values["cursor"]; ok {
		query.cursor = entries[0]
	}

	indexes, hasIndex := values["index"]
	rawValues, hasValue := values["value"]
	if hasIndex != hasValue {
		writeError(c, http.StatusBadRequest, "invalid_query", "Index and value must appear together")

		return docQuery{}, false
	}
	if !hasIndex {
		if query.cursor != "" {
			if err := validateID(query.cursor); err != nil {
				writeError(c, http.StatusBadRequest, "invalid_cursor", "Cursor is invalid")

				return docQuery{}, false
			}
		}

		return query, true
	}
	if err := validateName(indexes[0]); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_query", "Index is invalid")

		return docQuery{}, false
	}

	value, err := decodeQueryValue(rawValues[0])
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_query", "Value must be one JSON scalar")

		return docQuery{}, false
	}
	query.index = indexes[0]
	query.value = value
	query.indexed = true

	return query, true
}

func decodeQueryValue(raw string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values")
	}

	switch value.(type) {
	case nil, bool, json.Number, string:
		return value, nil
	default:
		return nil, errors.New("value must be scalar")
	}
}

func parseListQuery(c *gin.Context, validateCursor func(string) error) (int, string, bool) {
	limit := defaultListLimit
	if value, ok := c.GetQuery("limit"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maxListLimit {
			writeError(c, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 1000")

			return 0, "", false
		}
		limit = parsed
	}

	cursor := c.Query("cursor")
	if cursor != "" {
		if err := validateCursor(cursor); err != nil {
			writeError(c, http.StatusBadRequest, "invalid_cursor", "Cursor is invalid")

			return 0, "", false
		}
	}

	return limit, cursor, true
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
		a.fail(c, "delete database", err)

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
		a.fail(c, "generate document ID", err)

		return
	}
	rev, err := a.store.createDoc(database, id, document)
	if errors.Is(err, errDBNotFound) {
		writeError(c, http.StatusNotFound, "database_not_found", "Database does not exist")

		return
	}
	if errors.Is(err, errInvalidIndexValue) {
		writeError(c, http.StatusBadRequest, "invalid_document", "An indexed value exceeds the size limit")

		return
	}
	if err != nil {
		a.fail(c, "create document", err)

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
		a.fail(c, "read document", err)

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
	if errors.Is(err, errInvalidIndexValue) {
		writeError(c, http.StatusBadRequest, "invalid_document", "An indexed value exceeds the size limit")

		return
	}
	if err != nil {
		a.fail(c, "replace document", err)

		return
	}

	c.Header("ETag", formatETag(rev))
	c.Data(http.StatusOK, "application/json", document)
}

func (a *api) patchDoc(c *gin.Context) {
	mediaType, err := parsePatchType(c.GetHeader("Content-Type"))
	if err != nil {
		writeError(c, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must select a supported patch format")

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
		writeError(c, http.StatusBadRequest, "invalid_patch", "Could not read patch")

		return
	}

	doc, err := a.store.patchDoc(c.Param("database"), c.Param("id"), match, func(document []byte) ([]byte, error) {
		return applyPatch(document, body, mediaType, a.maxBodyBytes)
	})
	if errors.Is(err, errDocNotFound) {
		writeError(c, http.StatusNotFound, "document_not_found", "Document does not exist")

		return
	}
	if errors.Is(err, errPreconditionFailed) {
		writeError(c, http.StatusPreconditionFailed, "precondition_failed", "If-Match precondition failed")

		return
	}
	if errors.Is(err, errBodyTooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, "content_too_large", "Document exceeds the size limit")

		return
	}
	if errors.Is(err, errInvalidPatch) {
		writeError(c, http.StatusBadRequest, "invalid_patch", "Patch is invalid or produces a non-object document")

		return
	}
	if errors.Is(err, errInvalidIndexValue) {
		writeError(c, http.StatusBadRequest, "invalid_patch", "An indexed value exceeds the size limit")

		return
	}
	if err != nil {
		a.fail(c, "patch document", err)

		return
	}

	c.Header("ETag", formatETag(doc.revision))
	c.Data(http.StatusOK, "application/json", doc.json)
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
		a.fail(c, "delete document", err)

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

	var fields struct {
		Indexes json.RawMessage `json:"indexes"`
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return dbRequest{}, err
	}
	if bytes.Equal(bytes.TrimSpace(fields.Indexes), []byte("null")) {
		return dbRequest{}, errors.New("indexes must be an array")
	}

	return request, nil
}

func isJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)

	return err == nil && mediaType == "application/json"
}

func (a *api) fail(c *gin.Context, operation string, err error) {
	a.log.Error("request failed", "operation", operation, "method", c.Request.Method, "path", c.Request.URL.Path, "error", err)
	writeFailure(c, err)
}

func writeFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errCorruptData):
		writeError(c, http.StatusInternalServerError, "corrupt_data", "Stored data is corrupt")
	case isStoreUnavailable(err):
		writeError(c, http.StatusServiceUnavailable, "service_unavailable", "Service is unavailable")
	default:
		writeError(c, http.StatusInternalServerError, "internal_error", "Internal server error")
	}
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

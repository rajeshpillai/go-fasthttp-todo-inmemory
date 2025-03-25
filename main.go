package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

// Subtask represents a subtask for a todo.
type Subtask struct {
	ID        int    `json:"id,omitempty"`
	Title     string `json:"title"`
	Completed bool   `json:"completed"`
}

// Todo represents a todo item.
type Todo struct {
	ID          int       `json:"id,omitempty"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Completed   bool      `json:"completed"`
	Images      []string  `json:"images,omitempty"`
	Subtasks    []Subtask `json:"subtasks,omitempty"`
}

// Global in-memory state and a mutex for safe concurrent access.
var (
	todos  = make(map[int]*Todo)
	nextID = 1
	mu     sync.RWMutex
)

func main() {
	// Ensure the uploads directory exists.
	os.MkdirAll("uploads", os.ModePerm)

	log.Println("In-memory API server using fasthttp started on :8080")
	if err := fasthttp.ListenAndServe(":8080", requestHandler); err != nil {
		log.Fatalf("Error in ListenAndServe: %s", err)
	}
}

// requestHandler performs basic routing based on URL path and HTTP method.
func requestHandler(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	method := string(ctx.Method())

	if path == "/todos" {
		switch method {
		case "GET":
			getTodos(ctx)
		case "POST":
			createTodo(ctx)
		default:
			ctx.Error("Method not allowed", fasthttp.StatusMethodNotAllowed)
		}
		return
	}

	if strings.HasPrefix(path, "/todos/") {
		idStr := path[len("/todos/"):]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			ctx.Error("Invalid ID", fasthttp.StatusBadRequest)
			return
		}

		switch method {
		case "GET":
			getTodo(ctx, id)
		case "PUT":
			updateTodo(ctx, id)
		case "DELETE":
			deleteTodo(ctx, id)
		default:
			ctx.Error("Method not allowed", fasthttp.StatusMethodNotAllowed)
		}
		return
	}

	ctx.Error("Not found", fasthttp.StatusNotFound)
}

// getTodos returns all todos as a JSON array.
func getTodos(ctx *fasthttp.RequestCtx) {
	mu.RLock()
	defer mu.RUnlock()

	var list []Todo
	for _, todo := range todos {
		list = append(list, *todo)
	}

	resp, err := json.Marshal(list)
	if err != nil {
		ctx.Error(err.Error(), fasthttp.StatusInternalServerError)
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(resp)
}

// getTodo returns a single todo identified by its id.
func getTodo(ctx *fasthttp.RequestCtx, id int) {
	mu.RLock()
	todo, ok := todos[id]
	mu.RUnlock()

	if !ok {
		ctx.Error("Todo not found", fasthttp.StatusNotFound)
		return
	}

	resp, err := json.Marshal(todo)
	if err != nil {
		ctx.Error(err.Error(), fasthttp.StatusInternalServerError)
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(resp)
}

// createTodo handles POST /todos by parsing multipart/form-data,
// saving uploaded files, and adding the new todo to the in-memory state.
func createTodo(ctx *fasthttp.RequestCtx) {
	mForm, err := ctx.MultipartForm()
	if err != nil {
		ctx.Error(err.Error(), fasthttp.StatusBadRequest)
		return
	}

	// Retrieve text fields.
	title := ""
	if vals, ok := mForm.Value["title"]; ok && len(vals) > 0 {
		title = vals[0]
	}
	description := ""
	if vals, ok := mForm.Value["description"]; ok && len(vals) > 0 {
		description = vals[0]
	}
	subtasksStr := ""
	if vals, ok := mForm.Value["subtasks"]; ok && len(vals) > 0 {
		subtasksStr = vals[0]
	}

	var subtasks []Subtask
	if subtasksStr != "" {
		if err := json.Unmarshal([]byte(subtasksStr), &subtasks); err != nil {
			ctx.Error("Invalid subtasks format", fasthttp.StatusBadRequest)
			return
		}
	}

	// Process uploaded images.
	var images []string
	if files, ok := mForm.File["images"]; ok {
		for _, fileHeader := range files {
			savedPath, err := saveUploadedFile(fileHeader)
			if err != nil {
				ctx.Error(err.Error(), fasthttp.StatusInternalServerError)
				return
			}
			images = append(images, savedPath)
		}
	}

	// Determine Completed flag based on subtasks.
	completed := checkAllSubtasksCompleted(subtasks)

	// Create and store the new todo.
	mu.Lock()
	id := nextID
	nextID++
	newTodo := &Todo{
		ID:          id,
		Title:       title,
		Description: description,
		Completed:   completed,
		Images:      images,
		Subtasks:    subtasks,
	}
	todos[id] = newTodo
	mu.Unlock()

	resp, err := json.Marshal(newTodo)
	if err != nil {
		ctx.Error(err.Error(), fasthttp.StatusInternalServerError)
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusCreated)
	ctx.SetBody(resp)
}

// updateTodo handles PUT /todos/{id} to update an existing todo.
func updateTodo(ctx *fasthttp.RequestCtx, id int) {
	// First, check if the todo exists.
	mu.RLock()
	todo, ok := todos[id]
	mu.RUnlock()
	if !ok {
		ctx.Error("Todo not found", fasthttp.StatusNotFound)
		return
	}

	mForm, err := ctx.MultipartForm()
	if err != nil {
		ctx.Error(err.Error(), fasthttp.StatusBadRequest)
		return
	}

	// Update text fields if provided; otherwise keep existing values.
	title := todo.Title
	if vals, ok := mForm.Value["title"]; ok && len(vals) > 0 {
		title = vals[0]
	}
	description := todo.Description
	if vals, ok := mForm.Value["description"]; ok && len(vals) > 0 {
		description = vals[0]
	}
	subtasksStr := ""
	if vals, ok := mForm.Value["subtasks"]; ok && len(vals) > 0 {
		subtasksStr = vals[0]
	}

	var subtasks []Subtask
	if subtasksStr != "" {
		if err := json.Unmarshal([]byte(subtasksStr), &subtasks); err != nil {
			ctx.Error("Invalid subtasks format", fasthttp.StatusBadRequest)
			return
		}
	}

	// Process any newly uploaded images.
	var images []string
	if files, ok := mForm.File["images"]; ok {
		for _, fileHeader := range files {
			savedPath, err := saveUploadedFile(fileHeader)
			if err != nil {
				ctx.Error(err.Error(), fasthttp.StatusInternalServerError)
				return
			}
			images = append(images, savedPath)
		}
	}

	// Update the todo.
	mu.Lock()
	todo.Title = title
	todo.Description = description
	todo.Subtasks = subtasks
	todo.Images = images
	todo.Completed = checkAllSubtasksCompleted(subtasks)
	mu.Unlock()

	resp, err := json.Marshal(todo)
	if err != nil {
		ctx.Error(err.Error(), fasthttp.StatusInternalServerError)
		return
	}
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(resp)
}

// deleteTodo handles DELETE /todos/{id} by removing the todo from the in-memory state.
func deleteTodo(ctx *fasthttp.RequestCtx, id int) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := todos[id]; !ok {
		ctx.Error("Todo not found", fasthttp.StatusNotFound)
		return
	}
	delete(todos, id)
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

// checkAllSubtasksCompleted returns true if there is at least one subtask and all are completed.
func checkAllSubtasksCompleted(subtasks []Subtask) bool {
	if len(subtasks) == 0 {
		return false
	}
	for _, s := range subtasks {
		if !s.Completed {
			return false
		}
	}
	return true
}

// saveUploadedFile saves an uploaded file to disk (in the "uploads" folder) and returns its file path.
func saveUploadedFile(fileHeader *multipart.FileHeader) (string, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Create a unique filename using a timestamp.
	filename := fmt.Sprintf("%d_%s", time.Now().UnixNano(), fileHeader.Filename)
	filePath := filepath.Join("uploads", filename)
	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		return "", err
	}
	return filePath, nil
}


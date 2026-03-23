// Package api contains the API types for the todofor.ai backend.
package api

// Item represents a file or folder returned by the API.
type Item struct {
	ID           string `json:"id"`
	URI          string `json:"uri"`
	OriginalName string `json:"originalName"`
	MimeType     string `json:"mimeType"`
	FileSize     int64  `json:"fileSize"`
	CreatedAt    *int64 `json:"createdAt"`
	ModifiedAt   *int64 `json:"modifiedAt"`
}

// ListResult is the paginated response from the list endpoint.
type ListResult struct {
	Items         []Item `json:"items"`
	NextPageToken string `json:"nextPageToken,omitempty"`
}

// UploadResult is the response from the upload endpoint.
type UploadResult struct {
	AttachmentID string `json:"attachmentId"`
	URI          string `json:"uri"`
	FileSize     int64  `json:"fileSize"`
	CreatedAt    int64  `json:"createdAt"`
}

// MkdirRequest is the request body for creating a folder.
type MkdirRequest struct {
	ParentURI string `json:"parentUri"`
	Name      string `json:"name"`
}

// FolderMimeType is the MIME type used for folders.
const FolderMimeType = "application/vnd.todoforai.folder"

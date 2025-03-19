package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// TODO: implement the upload here
	const maxMemory int = 10 << 20
	r.ParseMultipartForm(int64(maxMemory))
	t_file, t_head, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't read thumbnail", err)
		return
	}
	defer t_file.Close()
	//t_type := t_head.Header.Get("Content-Type")

	media_type, _, err := mime.ParseMediaType(t_head.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "invalid media type", err)
		return
	}
	if media_type != "image/jpeg" && media_type != "image/png" {
		respondWithError(w, http.StatusBadRequest, "invalid media type", nil)
		return
	}
	//tn_b64 := base64.StdEncoding.EncodeToString(t_data)
	video_record, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find the video", err)
		return
	}
	if video_record.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "user is not owner of video", nil)
		return
	}
	tn_ext := strings.Split(media_type, "/")[1]
	ruid := make([]byte, 32)
	_, err = rand.Read(ruid)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate unique name", err)
		return
	}

	tn_fpath := filepath.Join(cfg.assetsRoot, base64.RawURLEncoding.EncodeToString(ruid)+"."+tn_ext)
	f, err := os.Create(tn_fpath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to store a file", err)
		return
	}
	defer f.Close()
	_, err = io.Copy(f, t_file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to store a file", err)
		return
	}

	//tn := thumbnail{
	//	data: t_data, mediaType: t_type,
	//}
	//videoThumbnails[videoID] = tn
	tn_path := fmt.Sprintf("http://localhost:%v/assets/%v.%v", cfg.port, base64.RawURLEncoding.EncodeToString(ruid), tn_ext)
	video_record.ThumbnailURL = &tn_path
	err = cfg.db.UpdateVideo(video_record)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to update the video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video_record)
}

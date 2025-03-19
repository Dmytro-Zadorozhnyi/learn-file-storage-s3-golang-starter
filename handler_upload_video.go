package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

type ffprobe_results struct {
	Streams []struct {
		Width  int `json:"width,omitempty"`
		Height int `json:"height,omitempty"`
	} `json:"streams"`
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
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

	video_record, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find the video", err)
		return
	}
	if video_record.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not owner of video", nil)
		return
	}

	fmt.Println("uploading video", videoID, "by user", userID)

	// TODO: implement the upload here
	const maxMemory int = 10 << 30

	r.ParseMultipartForm(int64(maxMemory))
	video_body, t_head, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't read video", err)
		return
	}
	defer video_body.Close()

	media_type, _, err := mime.ParseMediaType(t_head.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't parse mime type", err)
		return
	}
	if media_type != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "invalid media type", nil)
		return
	}
	//tn_ext := strings.Split(media_type, "/")[1]
	tmp_file, err := os.CreateTemp("", "tubely_upload*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to store a file", err)
		return
	}
	defer os.Remove(tmp_file.Name())
	defer tmp_file.Close()

	_, err = io.Copy(tmp_file, video_body)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to store a file", err)
		return
	}
	tmp_file.Seek(0, io.SeekStart)

	video_ratio, err := getVideoAspectRatio(tmp_file.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to get aspect ratio", err)
		return
	}

	video_fast_start, err := processVideoForFastStart(tmp_file.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to convert for fast start", err)
		return
	}
	fast_start_file, err := os.Open(video_fast_start)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to open fast start file", err)
		return
	}

	tn_ext := strings.Split(media_type, "/")[1]
	ruid := make([]byte, 32)
	_, err = rand.Read(ruid)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate unique name", err)
		return
	}

	s3_obj_key := fmt.Sprintf("%v/%v.%v", video_ratio, base64.RawURLEncoding.EncodeToString(ruid), tn_ext)

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &s3_obj_key,
		Body:        fast_start_file,
		ContentType: &media_type,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to store the video in s3", err)
		return
	}
	//tn := thumbnail{
	//	data: t_data, mediaType: t_type,
	//}
	//videoThumbnails[videoID] = tn
	//video_s3_url := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, s3_obj_key)
	video_s3_url := fmt.Sprintf("%s,%s", cfg.s3Bucket, s3_obj_key)
	video_record.VideoURL = &video_s3_url
	err = cfg.db.UpdateVideo(video_record)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to update the video", err)
		return
	}
	video_record, err = cfg.dbVideoToSignedVideo(video_record)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to get presigned s3 url", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video_record)
}

func getVideoAspectRatio(filePath string) (string, error) {

	get_aspect_cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var get_aspect_cmd_out bytes.Buffer
	get_aspect_cmd.Stdout = &get_aspect_cmd_out
	err := get_aspect_cmd.Run()
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(&get_aspect_cmd_out)
	var ffp_results ffprobe_results
	err = decoder.Decode(&ffp_results)
	if err != nil {
		return "", err
	}
	ratio := "other"
	if ffp_results.Streams[0].Height > 0 && ffp_results.Streams[0].Width > 0 {
		ratio = get_ratio_name(float32(ffp_results.Streams[0].Width) / float32(ffp_results.Streams[0].Height))
	}
	//log.Printf("h:%v, w:%v, ratio: %v", ffp_results.Streams[0].Height, ffp_results.Streams[0].Width, ratio)
	return ratio, nil
}

func get_ratio_name(ratio float32) string {
	if 1.59 <= ratio && ratio <= 1.82 {
		return "landscape"
	} else if 0.5 <= ratio && ratio <= 0.7 {
		return "portrait"
	} else {
		return "other"
	}
}

func processVideoForFastStart(filePath string) (string, error) {
	out := filePath + ".processing"
	convert_cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", out)
	var get_aspect_cmd_out bytes.Buffer
	convert_cmd.Stdout = &get_aspect_cmd_out
	err := convert_cmd.Run()
	if err != nil {
		//log.Print(convert_cmd.String())
		return "", err
	}
	return out, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	client := s3.NewPresignClient(s3Client)
	pres_req, err := client.PresignGetObject(context.Background(),
		&s3.GetObjectInput{
			Bucket: &bucket,
			Key:    &key,
		},
		s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}
	return pres_req.URL, nil
}

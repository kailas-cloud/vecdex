// Скачивание parquet файлов с HuggingFace Hub.
// Поддерживает resume через HTTP Range headers.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	hfAPIBase = "https://huggingface.co/api/datasets"
	hfDataset = "foursquare/fsq-os-places"
)

// downloader скачивает parquet файлы с HuggingFace.
type downloader struct {
	token   string
	dataDir string
	client  *http.Client
	metrics *loaderMetrics
}

func newDownloader(token, dataDir string, metrics *loaderMetrics) *downloader {
	return &downloader{
		token:   token,
		dataDir: dataDir,
		client: &http.Client{
			Timeout: 30 * time.Minute,
		},
		metrics: metrics,
	}
}

// hfParquetInfo — ответ HF API с URL'ами parquet файлов.
type hfParquetInfo struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

// DownloadPlaces скачивает parquet файлы places в dataDir/places/.
// maxFiles=0 — скачать все. Пропускает уже скачанные файлы.
func (d *downloader) DownloadPlaces(maxFiles int) error {
	return d.download("places", "train", maxFiles, filepath.Join(d.dataDir, "places"))
}

// DownloadCategories скачивает parquet файл categories.
func (d *downloader) DownloadCategories() (string, error) {
	outDir := filepath.Join(d.dataDir, "categories")
	files, err := d.downloadFiles("categories", "train", 1, outDir)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no category files downloaded")
	}
	return files[0], nil
}

func (d *downloader) download(config, split string, maxFiles int, outDir string) error {
	_, err := d.downloadFiles(config, split, maxFiles, outDir)
	return err
}

func (d *downloader) downloadFiles(
	config, split string, maxFiles int, outDir string,
) ([]string, error) {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	urls, err := d.listParquetFiles(config, split)
	if err != nil {
		return nil, fmt.Errorf("list parquet files: %w", err)
	}

	if maxFiles > 0 && len(urls) > maxFiles {
		urls = urls[:maxFiles]
	}

	log.Printf("downloading %d %s parquet files to %s", len(urls), config, outDir)

	var paths []string
	for i, info := range urls {
		name := fmt.Sprintf("%s-%05d.parquet", config, i)
		outPath := filepath.Join(outDir, name)
		paths = append(paths, outPath)

		if st, err := os.Stat(outPath); err == nil && st.Size() == info.Size {
			log.Printf("[%d/%d] %s: already downloaded (%d bytes)",
				i+1, len(urls), name, st.Size())
			continue
		}

		if err := d.downloadFile(info.URL, outPath, i+1, len(urls)); err != nil {
			return nil, fmt.Errorf("download %s: %w", name, err)
		}
	}

	return paths, nil
}

// listParquetFiles получает URL'ы parquet файлов из HF API.
func (d *downloader) listParquetFiles(config, split string) ([]hfParquetInfo, error) {
	url := fmt.Sprintf("%s/%s/parquet/%s/%s", hfAPIBase, hfDataset, config, split)

	req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HF API request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HF API: status %d: %s", resp.StatusCode, string(body))
	}

	var files []hfParquetInfo
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("parse HF response: %w", err)
	}

	var parquets []hfParquetInfo
	for _, f := range files {
		if strings.HasSuffix(f.Filename, ".parquet") ||
			strings.HasSuffix(f.URL, ".parquet") {
			parquets = append(parquets, f)
		}
	}

	return parquets, nil
}

// downloadFile скачивает один файл с поддержкой resume (HTTP Range).
func (d *downloader) downloadFile(url, outPath string, num, total int) error {
	cleanPath := filepath.Clean(outPath)
	tmpPath := cleanPath + ".tmp"

	var offset int64
	if st, err := os.Stat(tmpPath); err == nil {
		offset = st.Size()
	}

	req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		log.Printf("[%d/%d] resuming from %d bytes", num, total, offset)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	flags := os.O_WRONLY | os.O_CREATE
	if resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}

	f, err := os.OpenFile(tmpPath, flags, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}

	written, err := io.Copy(f, &progressReader{
		reader:  resp.Body,
		total:   resp.ContentLength + offset,
		current: offset,
		name:    filepath.Base(outPath),
		num:     num,
		count:   total,
		metrics: d.metrics,
	})
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	log.Printf("[%d/%d] %s: downloaded %d bytes",
		num, total, filepath.Base(outPath), offset+written)

	if err := os.Rename(tmpPath, cleanPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// progressReader логирует прогресс скачивания и обновляет метрики.
type progressReader struct {
	reader  io.Reader
	total   int64
	current int64
	name    string
	num     int
	count   int
	metrics *loaderMetrics
	lastLog time.Time
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.current += int64(n)

	if pr.metrics != nil {
		pr.metrics.downloadBytes.Add(float64(n))
	}

	if time.Since(pr.lastLog) > 5*time.Second {
		pr.lastLog = time.Now()
		if pr.total > 0 {
			pct := float64(pr.current) / float64(pr.total) * 100
			log.Printf("[%d/%d] %s: %.1f%% (%d/%d MB)",
				pr.num, pr.count, pr.name, pct,
				pr.current/1024/1024, pr.total/1024/1024)
		}
	}

	if err != nil {
		return n, fmt.Errorf("read: %w", err)
	}
	return n, nil
}

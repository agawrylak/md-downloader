package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type Config struct {
	AccessToken string
	Repos       []string
	Output      string
	History     string
	Ignore      map[string][]string
}

type History struct {
	Files map[string]string `json:"files"`
}

var cfg Config
var ignore []string
var log *logrus.Logger

func main() {
	log = logrus.New()
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	var rootCmd = &cobra.Command{
		Use:   "md-reader",
		Short: "MD Reader is a tool for downloading .md files from repositories",
		Long:  `MD Reader is a tool for downloading .md files from repositories`,
		Run: func(cmd *cobra.Command, args []string) {
			parseIgnorePaths()
			for _, repo := range cfg.Repos {
				listMdFiles(repo)
			}
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfg.AccessToken, "access-token", "", "Github Access Token")
	rootCmd.PersistentFlags().StringSliceVar(&cfg.Repos, "repo", []string{}, "Github Repositories")
	rootCmd.PersistentFlags().StringVar(&cfg.Output, "output", "docs", "Output Directory")
	rootCmd.PersistentFlags().StringVar(&cfg.History, "history", "history.json", "History File")
	rootCmd.PersistentFlags().StringSliceVar(&ignore, "ignore", []string{}, "Ignore paths")

	rootCmd.Execute()
}

func parseIgnorePaths() {
	cfg.Ignore = make(map[string][]string)
	for _, i := range ignore {
		split := strings.SplitN(i, ":", 2)
		if len(split) < 2 {
			log.Errorf("Invalid ignore path: %s\n", i)
			continue
		}
		repo := split[0]
		paths := strings.Split(split[1], ",")
		cfg.Ignore[repo] = paths
	}
}

func listMdFiles(repo string) {
	apiURL := "https://api.github.com"
	repo = strings.TrimPrefix(repo, "https://github.com/")
	contentsURL := fmt.Sprintf("%s/repos/%s/git/trees/master?recursive=1", apiURL, repo)

	client := &http.Client{}
	req, _ := http.NewRequest("GET", contentsURL, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)

	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("Failed to send request: %s\n", err)
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Failed to read response body: %s\n", err)
		return
	}
	bodyString := string(bodyBytes)
	log.Debugf("Response body: %s\n", bodyString)

	var contents struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			Sha  string `json:"sha"`
			Size int    `json:"size"`
			Url  string `json:"url"`
		} `json:"tree"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&contents); err != nil {
		log.Errorf("Failed to decode response JSON: %s\n", err)
		return
	}

	history := loadHistory()

	for _, item := range contents.Tree {
		if item.Type == "blob" && filepath.Ext(item.Path) == ".md" {
			if shouldDownload(item.Path, item.Sha, history) {
				if isIgnored(repo, item.Path) {
					log.Infof("Ignoring file: %s\n", item.Path)
				} else {
					log.Infof("Downloading file: %s\n", item.Path)
					// Get the file content through GitHub API
					req, _ = http.NewRequest("GET", item.Url, nil)
					req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
					resp, err := client.Do(req)
					if err != nil {
						log.Errorf("Failed to send request: %s\n", err)
						history.Files[item.Path] = "ERROR"
						continue
					}
					defer resp.Body.Close()

					var fileContentResponse struct {
						Content string `json:"content"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&fileContentResponse); err != nil {
						log.Errorf("Failed to decode response JSON: %s\n", err)
						history.Files[item.Path] = "ERROR"
						continue
					}
					decodedContent, err := base64.StdEncoding.DecodeString(fileContentResponse.Content)
					if err != nil {
						log.Errorf("Failed to decode base64 content: %s\n", err)
						history.Files[item.Path] = "ERROR"
						continue
					}

					saveFile(repo, item.Path, string(decodedContent))
					history.Files[item.Path] = item.Sha
				}
			} else {
				log.Infof("Skipping file: %s (already up to date)\n", item.Path)
			}
		}
	}

	saveHistory(history)
}

func saveFile(repo, filePath, content string) {
	fileDir := filepath.Join(cfg.Output, filepath.Base(repo)) // Use only the repository name, skip the username
	err := os.MkdirAll(fileDir, os.ModePerm)
	if err != nil {
		log.Errorf("Failed to create directory: %s\n", fileDir)
		return
	}

	filePath = filepath.Join(fileDir, filePath)

	out, err := os.Create(filePath)
	if err != nil {
		log.Errorf("Failed to create file: %s\n", filePath)
		return
	}
	defer out.Close()

	_, err = out.WriteString(content)
	if err != nil {
		log.Errorf("Failed to save file: %s\n", filePath)
		return
	}

	log.Infof("File downloaded: %s\n", filePath)
}

func shouldDownload(filePath, sha string, history History) bool {
	if lastSha, ok := history.Files[filePath]; !ok || lastSha == "ERROR" {
		return true
	}

	return history.Files[filePath] != sha
}

func isIgnored(repo, filePath string) bool {
	if ignorePaths, ok := cfg.Ignore[repo]; ok {
		for _, path := range ignorePaths {
			if path == filePath {
				return true
			}
		}
	}
	return false
}

func loadHistory() History {
	history := History{
		Files: make(map[string]string),
	}

	file, err := os.Open(cfg.History)
	if err != nil {
		log.Warnf("Failed to open history file: %s\n", err)
		return history
	}
	defer file.Close()

	err = json.NewDecoder(file).Decode(&history)
	if err != nil {
		log.Warnf("Failed to parse history file: %s\n", cfg.History)
	}

	return history
}

func saveHistory(history History) {
	file, err := os.Create(cfg.History)
	if err != nil {
		log.Errorf("Failed to create history file: %s\n", cfg.History)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "    ")
	err = encoder.Encode(history)
	if err != nil {
		log.Errorf("Failed to save history file: %s\n", cfg.History)
	}
}

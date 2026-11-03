package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/furmanp/gitlab-activity-importer/internal"
)

func GetGitlabUser() (internal.GitLabUser, error) {
	url := os.Getenv("BASE_URL")

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), "GET", fmt.Sprintf("%v/api/v4/user", url), nil)
	if err != nil {
		return internal.GitLabUser{}, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("PRIVATE-TOKEN", os.Getenv("GITLAB_TOKEN"))

	res, err := client.Do(req)
	if err != nil {
		return internal.GitLabUser{}, fmt.Errorf("error making the request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return internal.GitLabUser{}, fmt.Errorf("status %d: %s", res.StatusCode, string(body))
	}

	var user internal.GitLabUser
	if err := json.NewDecoder(res.Body).Decode(&user); err != nil {
		return internal.GitLabUser{}, fmt.Errorf("decode error: %w", err)
	}

	return user, nil
}

func GetUsersProjectsIds(userId int) ([]int, error) {
	base := os.Getenv("BASE_URL")
	token := os.Getenv("GITLAB_TOKEN")

	allProjectIds := make([]int, 0, 128)
	client := &http.Client{Timeout: 30 * time.Second}

	for page := 1; ; {
		req, err := http.NewRequestWithContext(context.Background(),
			"GET",
			fmt.Sprintf("%s/api/v4/users/%d/contributed_projects?per_page=100&page=%d", base, userId, page),
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("do request: %w", err)
		}

		var next string
		func() {
			defer res.Body.Close()

			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				err = fmt.Errorf("request failed with status code: %d: %s", res.StatusCode, string(body))
				return
			}

			var projects []struct {
				ID int `json:"id"`
			}
			if derr := json.NewDecoder(res.Body).Decode(&projects); derr != nil {
				err = fmt.Errorf("error parsing JSON: %w", derr)
				return
			}

			for _, p := range projects {
				allProjectIds = append(allProjectIds, p.ID)
			}

			next = res.Header.Get("X-Next-Page")
		}()
		if err != nil {
			return nil, err
		}

		if next == "" {
			break
		}
		n, convErr := strconv.Atoi(next)
		if convErr != nil || n <= page {
			break
		}
		page = n
	}

	return allProjectIds, nil
}

func GetProjectCommits(projectId int, gitlabUserName string) ([]internal.Commit, error) {
	allCommits, err := fetchProjectCommits(projectId, gitlabUserName, "")
	if err != nil {
		return nil, err
	}
	if len(allCommits) == 0 {
		return nil, fmt.Errorf("found no commits in project no.:%v", projectId)
	}
	return allCommits, nil
}

// GetProjectCommitsAllBranches returns the user's commits from every branch of the project,
// not only the default one, so work on unmerged or squash-merged branches is not lost.
func GetProjectCommitsAllBranches(projectId int, gitlabUserName string) ([]internal.Commit, error) {
	branches, err := GetProjectBranches(projectId)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var allCommits []internal.Commit
	// An empty ref means the default branch, which covers repositories without listed branches.
	for _, ref := range append([]string{""}, branches...) {
		commits, err := fetchProjectCommits(projectId, gitlabUserName, ref)
		if err != nil {
			if ref == "" {
				return nil, err
			}
			log.Printf("Skipping branch %q of project %d: %v", ref, projectId, err)
			continue
		}
		for _, c := range commits {
			if !seen[c.ID] {
				seen[c.ID] = true
				allCommits = append(allCommits, c)
			}
		}
	}
	if len(allCommits) == 0 {
		return nil, fmt.Errorf("found no commits in project no.:%v", projectId)
	}
	return allCommits, nil
}

func GetProjectBranches(projectId int) ([]string, error) {
	base := os.Getenv("BASE_URL")
	token := os.Getenv("GITLAB_TOKEN")

	var branches []string
	client := &http.Client{Timeout: 30 * time.Second}
	for page := 1; ; {
		req, err := http.NewRequestWithContext(context.Background(), "GET",
			fmt.Sprintf("%s/api/v4/projects/%d/repository/branches?per_page=100&page=%d", base, projectId, page), nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("do request: %w", err)
		}

		var next string
		func() {
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				err = fmt.Errorf("request failed with status code: %d: %s", res.StatusCode, string(body))
				return
			}
			var batch []struct {
				Name string `json:"name"`
			}
			if derr := json.NewDecoder(res.Body).Decode(&batch); derr != nil {
				err = fmt.Errorf("error parsing JSON: %w", derr)
				return
			}
			for _, b := range batch {
				branches = append(branches, b.Name)
			}
			next = res.Header.Get("X-Next-Page")
		}()
		if err != nil {
			return nil, err
		}
		if next == "" {
			break
		}
		n, convErr := strconv.Atoi(next)
		if convErr != nil || n <= page {
			break
		}
		page = n
	}
	return branches, nil
}

func fetchProjectCommits(projectId int, gitlabUserName string, ref string) ([]internal.Commit, error) {
	base := os.Getenv("BASE_URL")
	token := os.Getenv("GITLAB_TOKEN")

	refParam := ""
	if ref != "" {
		refParam = "&ref_name=" + url.QueryEscape(ref)
	}

	var allCommits []internal.Commit
	client := &http.Client{Timeout: 30 * time.Second}
	for page := 1; ; {
		req, err := http.NewRequestWithContext(context.Background(), "GET",
			fmt.Sprintf("%s/api/v4/projects/%d/repository/commits?author=%s%s&per_page=100&page=%d",
				base, projectId, url.QueryEscape(gitlabUserName), refParam, page), nil)
		if err != nil {
			return nil, fmt.Errorf("error fetching the commits: %w", err)
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("do request: %w", err)
		}

		var next string
		func() {
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				err = fmt.Errorf("request failed with status code: %d: %s", res.StatusCode, string(body))
				return
			}
			var batch []internal.Commit
			if derr := json.NewDecoder(res.Body).Decode(&batch); derr != nil {
				err = fmt.Errorf("error parsing JSON: %w", derr)
				return
			}

			allCommits = append(allCommits, batch...)
			next = res.Header.Get("X-Next-Page")
		}()
		if err != nil {
			return nil, err
		}
		if next == "" {
			break
		}
		n, convErr := strconv.Atoi(next)
		if convErr != nil || n <= page {
			break
		}
		page = n
	}
	return allCommits, nil
}

// skippedEventActions are events that are not imported: pushes are already covered by the
// imported commits, and membership changes are not contributions.
var skippedEventActions = map[string]bool{
	"pushed to":  true,
	"pushed new": true,
	"deleted":    true,
	"joined":     true,
	"left":       true,
	"expired":    true,
}

// GetUserEvents returns the user's non-push GitLab activity (merge requests, issues, comments,
// approvals, ...) as commits, so it shows up in the GitHub contribution graph as well.
func GetUserEvents(userId int) ([]internal.Commit, error) {
	base := os.Getenv("BASE_URL")
	token := os.Getenv("GITLAB_TOKEN")

	var events []internal.Commit
	client := &http.Client{Timeout: 30 * time.Second}
	for page := 1; ; {
		req, err := http.NewRequestWithContext(context.Background(), "GET",
			fmt.Sprintf("%s/api/v4/users/%d/events?per_page=100&page=%d", base, userId, page), nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("do request: %w", err)
		}

		var next string
		func() {
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				err = fmt.Errorf("request failed with status code: %d: %s", res.StatusCode, string(body))
				return
			}
			var batch []internal.GitLabEvent
			if derr := json.NewDecoder(res.Body).Decode(&batch); derr != nil {
				err = fmt.Errorf("error parsing JSON: %w", derr)
				return
			}
			for _, e := range batch {
				if e.PushData != nil || skippedEventActions[e.ActionName] {
					continue
				}
				events = append(events, internal.Commit{
					// The ID becomes the commit message, which is what deduplicates imports.
					ID:           fmt.Sprintf("gitlab-event-%d", e.ID),
					Message:      fmt.Sprintf("%s %s", e.ActionName, e.TargetType),
					AuthoredDate: e.CreatedAt,
				})
			}
			next = res.Header.Get("X-Next-Page")
		}()
		if err != nil {
			return nil, err
		}
		if next == "" {
			break
		}
		n, convErr := strconv.Atoi(next)
		if convErr != nil || n <= page {
			break
		}
		page = n
	}
	return events, nil
}

func FetchAllCommits(projectIds []int, gitlabUserName string, commitChannel chan []internal.Commit) {
	var wg sync.WaitGroup
	var validCommitsFound atomic.Bool

	for _, projectId := range projectIds {
		wg.Add(1)

		go func(projId int) {
			defer wg.Done()

			commits, err := GetProjectCommitsAllBranches(projId, gitlabUserName)
			if err != nil {
				log.Printf("Error fetching commits for project %d: %v", projId, err)
				return
			}
			if len(commits) > 0 {
				commitChannel <- commits
				validCommitsFound.Store(true)
			}

		}(projectId)
	}

	wg.Wait()

	if !validCommitsFound.Load() {
		log.Println("No valid commits found across any projects")
	}

	close(commitChannel)
}

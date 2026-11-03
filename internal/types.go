package internal

import (
	"fmt"
	"time"
)

type Commit struct {
	ID           string    `json:"id"`
	Message      string    `json:"message"`
	AuthorName   string    `json:"author_name"`
	AuthorMail   string    `json:"author_email"`
	AuthoredDate time.Time `json:"authored_date"`
}

type GitLabEvent struct {
	ID         int       `json:"id"`
	ActionName string    `json:"action_name"`
	TargetType string    `json:"target_type"`
	CreatedAt  time.Time `json:"created_at"`
	PushData   *struct {
		CommitCount int `json:"commit_count"`
	} `json:"push_data"`
}

type GitLabUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

func (c Commit) Print() {
	fmt.Printf("Commit Details:\n")
	fmt.Printf("ID           : %s\n", c.ID)
	fmt.Printf("Message      : %s\n", c.Message)
	fmt.Printf("Author Name  : %s\n", c.AuthorName)
	fmt.Printf("Author Email : %s\n", c.AuthorMail)
	fmt.Printf("Authored Date: %s\n", c.AuthoredDate)
}

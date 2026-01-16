package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Morditux/dbsession"
)

func main() {
	// Initialize SQLite store
	store, err := dbsession.NewSQLiteStore("sessions.db")
	if err != nil {
		log.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Alternative: Initialize PostgreSQL store
	// store, err := dbsession.NewPostgreSQLStore("postgres://user:password@localhost/dbname?sslmode=disable")
	// if err != nil {
	// 	log.Fatalf("failed to create store: %v", err)
	// }
	// defer store.Close()

	// Initialize Manager with 1 hour TTL and 5 minutes cleanup interval
	mgr := dbsession.NewManager(dbsession.Config{
		Store:           store,
		TTL:             time.Hour,
		CookieName:      "my_app_session",
		CleanupInterval: 5 * time.Minute,
	})
	defer mgr.Close()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		session, err := mgr.Get(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Use thread-safe Get/Set methods
		count := 0
		if val, ok := session.Get("count"); ok {
			if c, ok := val.(int); ok {
				count = c
			}
		}
		count++
		session.Set("count", count)

		// Save session
		if err := mgr.Save(w, r, session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, "Hello! You have visited this page %d times.", count)
	})

	http.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		session, err := mgr.Get(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := mgr.Destroy(w, r, session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Fprint(w, "Logged out!")
	})

	fmt.Println("Server starting on :8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

package updater

import (
	"context"
	"fmt"

	"github.com/anchel/rathole-go/internal/config"
	"github.com/fsnotify/fsnotify"
)

func GetUpdater(ctx context.Context, filePath string) (chan *config.Config, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	err = watcher.Add(filePath)
	if err != nil {
		return nil, err
	}

	updater := make(chan *config.Config)

	go func() {
		defer func() {
			fmt.Println("updater close")
		}()
		defer close(updater)
		defer watcher.Close()
		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// fmt.Println("event:", event)
				if event.Has(fsnotify.Write) {
					fmt.Println("modified file:", event.Name)
					conf, err := config.GetConfig()
					if err != nil {
						fmt.Println("updater read config fail", err)
					} else {
						select {
						case <-ctx.Done():
							return
						case updater <- conf:
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				fmt.Println("error:", err)
			}
		}
	}()

	return updater, nil
}

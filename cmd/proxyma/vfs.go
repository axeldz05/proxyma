package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	proxyma_bind "proxyma/cmd/proxyma-bind"

	"github.com/spf13/cobra"
)

var vfsStorage string

var vfsCmd = &cobra.Command{
	Use:   "vfs",
	Short: "Manages virtual file system snapshots and subscriptions",
}

var vfsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all files in the virtual file system snapshot",
	RunE: func(cmd *cobra.Command, args []string) error {
		_ = loadConfigOrDie(vfsStorage)
		proxyma_bind.SetStoragePath(vfsStorage)

		jsonStr := proxyma_bind.GetVFSFilesJson()
		if strings.Contains(jsonStr, `"error":`) {
			type ErrResp struct {
				Error string `json:"error"`
			}
			var errR ErrResp
			_ = json.Unmarshal([]byte(jsonStr), &errR)
			return fmt.Errorf("%s", errR.Error)
		}
		var list []proxyma_bind.VFSFileStatus
		if err := json.Unmarshal([]byte(jsonStr), &list); err != nil {
			return fmt.Errorf("failed to parse files: %w", err)
		}

		if len(list) == 0 {
			fmt.Println("No files in the VFS snapshot.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSIZE\tSUBSCRIBED\tLOCAL\tSTATUS\tHASH")
		for _, entry := range list {
			status := "Active"
			if entry.Deleted {
				status = "Deleted"
			}
			sizeStr := fmt.Sprintf("%d B", entry.Size)
			if entry.Size >= 1024*1024 {
				sizeStr = fmt.Sprintf("%.2f MB", float64(entry.Size)/(1024*1024))
			} else if entry.Size >= 1024 {
				sizeStr = fmt.Sprintf("%.2f KB", float64(entry.Size)/1024)
			}

			_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%t\t%t\t%s\t%s\n",
				entry.Name,
				entry.Version,
				sizeStr,
				entry.Subscribed,
				entry.HasLocal,
				status,
				entry.Hash,
			)
		}
		_ = w.Flush()
		return nil
	},
}

var vfsUploadCmd = &cobra.Command{
	Use:   "upload [filepath] [name]",
	Short: "Upload a local file into the VFS registry",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		filePath := args[0]
		absPath, err := filepath.Abs(filePath)
		if err != nil {
			return fmt.Errorf("invalid path: %w", err)
		}

		name := filepath.Base(absPath)
		if len(args) == 2 {
			name = args[1]
		}

		_ = loadConfigOrDie(vfsStorage)
		proxyma_bind.SetStoragePath(vfsStorage)

		errStr := proxyma_bind.UploadFile(name, absPath)
		if errStr != "" {
			return fmt.Errorf("%s", errStr)
		}

		fmt.Printf("✅ File '%s' uploaded successfully to VFS.\n", name)
		return nil
	},
}

var vfsSubscribeCmd = &cobra.Command{
	Use:   "subscribe [filename]",
	Short: "Subscribe to download updates for a VFS file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_ = loadConfigOrDie(vfsStorage)
		proxyma_bind.SetStoragePath(vfsStorage)

		errStr := proxyma_bind.SetSubscription(name, true)
		if errStr != "" {
			return fmt.Errorf("%s", errStr)
		}

		fmt.Printf("✅ Subscribed to file '%s'. Synchronization triggered.\n", name)
		return nil
	},
}

var vfsUnsubscribeCmd = &cobra.Command{
	Use:   "unsubscribe [filename]",
	Short: "Unsubscribe from updates for a VFS file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_ = loadConfigOrDie(vfsStorage)
		proxyma_bind.SetStoragePath(vfsStorage)

		errStr := proxyma_bind.SetSubscription(name, false)
		if errStr != "" {
			return fmt.Errorf("%s", errStr)
		}

		fmt.Printf("✅ Unsubscribed from file '%s'.\n", name)
		return nil
	},
}

var vfsDeleteCmd = &cobra.Command{
	Use:   "delete [filename]",
	Short: "Mark a file as deleted in the VFS registry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_ = loadConfigOrDie(vfsStorage)
		proxyma_bind.SetStoragePath(vfsStorage)

		errStr := proxyma_bind.DeleteFile(name)
		if errStr != "" {
			return fmt.Errorf("%s", errStr)
		}

		fmt.Printf("✅ File '%s' marked as deleted in VFS registry.\n", name)
		return nil
	},
}

var vfsPurgeCmd = &cobra.Command{
	Use:   "purge [filename]",
	Short: "Purge the local physical cache of a VFS file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_ = loadConfigOrDie(vfsStorage)
		proxyma_bind.SetStoragePath(vfsStorage)

		errStr := proxyma_bind.DeleteLocalCache(name)
		if errStr != "" {
			return fmt.Errorf("%s", errStr)
		}

		fmt.Printf("✅ Physical cache for file '%s' purged from disk.\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(vfsCmd)
	vfsCmd.AddCommand(vfsListCmd)
	vfsCmd.AddCommand(vfsUploadCmd)
	vfsCmd.AddCommand(vfsSubscribeCmd)
	vfsCmd.AddCommand(vfsUnsubscribeCmd)
	vfsCmd.AddCommand(vfsDeleteCmd)
	vfsCmd.AddCommand(vfsPurgeCmd)

	defaultStorage := getDefaultStorage()
	vfsCmd.PersistentFlags().StringVar(&vfsStorage, "storage", defaultStorage, "Path to the local node's directory")
}

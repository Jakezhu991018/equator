package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/zion/go/support/db"
	"github.com/zion/equator/db2/schema"
	"github.com/zion/equator/ingest"
	hlog "github.com/zion/equator/log"
)

var dbCmd = &cobra.Command{
	Use:   "db [command]",
	Short: "commands to manage equator's postgres db",
}

var dbClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "clears all imported historical data",
	Run: func(cmd *cobra.Command, args []string) {
		initConfig()
		hlog.DefaultLogger.Logger.Level = config.LogLevel

		i := ingestSystem()
		err := i.ClearAll()
		if err != nil {
			hlog.Error(err)
			os.Exit(1)
		}
	},
}

var dbInitCmd = &cobra.Command{
	Use:   "init",
	Short: "install schema",
	Long:  "init initializes the postgres database used by equator.",
	Run: func(cmd *cobra.Command, args []string) {
		db, err := db.Open("postgres", viper.GetString("db-url"))
		if err != nil {
			hlog.Error(err)
			os.Exit(1)
		}

		err = schema.Init(db)
		if err != nil {
			hlog.Error(err)
			os.Exit(1)
		}
	},
}

var dbMigrateCmd = &cobra.Command{
	Use:   "migrate [up|down|redo] [COUNT]",
	Short: "migrate schema",
	Long:  "performs a schema migration command",
	Run: func(cmd *cobra.Command, args []string) {

		// Allow invokations with 1 or 2 args.  All other args counts are erroneous.
		if len(args) < 1 || len(args) > 2 {
			cmd.Usage()
			os.Exit(1)
		}

		dir := schema.MigrateDir(args[0])
		count := 0

		// If a second arg is present, parse it to an int and use it as the count
		// argument to the migration call.
		if len(args) == 2 {
			var err error
			count, err = strconv.Atoi(args[1])
			if err != nil {
				log.Println(err)
				cmd.Usage()
				os.Exit(1)
			}
		}

		db, err := sql.Open("postgres", viper.GetString("db-url"))
		if err != nil {
			log.Fatal(err)
		}

		_, err = schema.Migrate(db, dir, count)
		if err != nil {
			log.Fatal(err)
		}
	},
}

var dbReapCmd = &cobra.Command{
	Use:   "reap",
	Short: "reaps (i.e. removes) any reapable history data",
	Long:  "reap removes any historical data that is earlier than the configured retention cutoff",
	Run: func(cmd *cobra.Command, args []string) {
		initApp(cmd, args)

		err := app.DeleteUnretainedHistory()
		if err != nil {
			log.Fatal(err)
		}
	},
}

var dbReingestCmd = &cobra.Command{
	Use:   "reingest",
	Short: "imports all data",
	Long:  "reingest runs the ingestion pipeline over every ledger",
	Run: func(cmd *cobra.Command, args []string) {
		initConfig()
		hlog.DefaultLogger.Logger.Level = config.LogLevel

		i := ingestSystem()
		i.SkipCursorUpdate = true
		logStatus := func(stage string) {
			count := i.Metrics.IngestLedgerTimer.Count()
			rate := i.Metrics.IngestLedgerTimer.RateMean()
			loadMean := time.Duration(i.Metrics.LoadLedgerTimer.Mean())
			ingestMean := time.Duration(i.Metrics.IngestLedgerTimer.Mean())
			clearMean := time.Duration(i.Metrics.IngestLedgerTimer.Mean())
			hlog.
				WithField("count", count).
				WithField("rate", rate).
				WithField("means", fmt.Sprintf("load: %s clear: %s ingest: %s", loadMean, clearMean, ingestMean)).
				Infof("reingest: %s", stage)
		}

		done := make(chan error, 1)

		// run ingestion in separate goroutine
		go func() {
			_, err := reingest(i, args)
			done <- err
			logStatus("complete")
		}()

		// output metrics
		metrics := time.Tick(2 * time.Second)
		for {
			select {
			case <-metrics:
				logStatus("status")

			case err := <-done:
				if err != nil {
					log.Fatal(err)
				}
				os.Exit(0)
			}
		}
	},
}

func init() {
	dbCmd.AddCommand(dbInitCmd)
	dbCmd.AddCommand(dbClearCmd)
	dbCmd.AddCommand(dbMigrateCmd)
	dbCmd.AddCommand(dbReapCmd)
	dbCmd.AddCommand(dbReingestCmd)
}

func ingestSystem() *ingest.System {
	hdb, err := db.Open("postgres", config.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}

	cdb, err := db.Open("postgres", config.ZionCoreDatabaseURL)
	if err != nil {
		log.Fatal(err)
	}

	passphrase := viper.GetString("network-passphrase")
	if passphrase == "" {
		log.Fatal("network-passphrase is blank: reingestion requires manually setting passphrase")
	}

	i := ingest.New(passphrase, config.ZionCoreURL, cdb, hdb)
	return i
}

func reingest(i *ingest.System, args []string) (int, error) {
	if len(args) == 0 {
		count, err := i.ReingestAll()
		return count, err
	}

	if len(args) == 1 && args[0] == "outdated" {
		count, err := i.ReingestOutdated()
		return count, err
	}

	for idx, arg := range args {
		seq, err := strconv.Atoi(arg)
		if err != nil {
			return idx, err
		}

		err = i.ReingestSingle(int32(seq))
		if err != nil {
			return idx, err
		}
	}
	return len(args), nil
}

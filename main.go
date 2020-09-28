package main

import (
	"context"
	"github.com/Kugelschieber/marvinblum.de/blog"
	"github.com/Kugelschieber/marvinblum.de/db"
	"github.com/Kugelschieber/marvinblum.de/tpl"
	"github.com/Kugelschieber/marvinblum.de/tracking"
	"github.com/NYTimes/gziphandler"
	emvi "github.com/emvi/api-go"
	"github.com/emvi/logbuch"
	"github.com/emvi/pirsch"
	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"github.com/rs/cors"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

const (
	staticDir       = "static"
	staticDirPrefix = "/static/"
	logTimeFormat   = "2006-01-02_15:04:05"
	envPrefix       = "MB_"
	shutdownTimeout = time.Second * 30
)

var (
	tracker      *pirsch.Tracker
	tplCache     *tpl.Cache
	blogInstance *blog.Blog
)

func serveAbout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker.Hit(r, nil)
		tplCache.Render(w, "about.html", struct {
			Articles []emvi.Article
		}{
			blogInstance.GetLatestArticles(),
		})
	}
}

func serveLegal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker.Hit(r, nil)
		tplCache.Render(w, "legal.html", nil)
	}
}

func serveBlogPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker.Hit(r, nil)
		tplCache.Render(w, "blog.html", struct {
			Articles map[int][]emvi.Article
		}{
			blogInstance.GetArticles(),
		})
	}
}

func serveBlogArticle() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		slug := strings.Split(vars["slug"], "-")

		if len(slug) == 0 {
			http.Redirect(w, r, "/notfound", http.StatusFound)
			return
		}

		article := blogInstance.GetArticle(slug[len(slug)-1])

		if len(article.Id) == 0 {
			http.Redirect(w, r, "/notfound", http.StatusFound)
			return
		}

		// track the hit if the article was found, otherwise we don't care
		tracker.Hit(r, nil)

		tplCache.RenderWithoutCache(w, "article.html", struct {
			Title     string
			Content   template.HTML
			Published time.Time
		}{
			article.LatestArticleContent.Title,
			template.HTML(article.LatestArticleContent.Content),
			article.Published,
		})
	}
}

func serveTracking() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker.Hit(r, nil)
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))

		if start > 365 {
			start = 365
		} else if start < 7 {
			start = 7
		}

		var startDate, endDate time.Time

		if err := r.ParseForm(); err != nil {
			logbuch.Warn("Error parsing tracking form", logbuch.Fields{"err": err})
		} else {
			startDate, _ = time.Parse("2006-01-02", r.FormValue("start-date"))
			endDate, _ = time.Parse("2006-01-02", r.FormValue("end-date"))
		}

		if startDate.IsZero() || endDate.IsZero() {
			startDate = time.Now().UTC().Add(-time.Hour * 24 * time.Duration(start))
			endDate = time.Now().UTC()
		}

		activeVisitorPages, activeVisitors := tracking.GetActiveVisitors()
		totalVisitorsLabels, totalVisitorsDps, sessionsDps, bouncesDps := tracking.GetTotalVisitors(startDate, endDate)
		hourlyVisitorsTodayLabels, hourlyVisitorsTodayDps := tracking.GetHourlyVisitorsToday()
		pageVisitors, pageRank := tracking.GetPageVisits(startDate, endDate)
		timeOfDay, timeOfDayMax := tracking.GetVisitorTimeOfDay(startDate, endDate)
		tplCache.RenderWithoutCache(w, "tracking.html", struct {
			Start                     int
			StartDate                 time.Time
			EndDate                   time.Time
			TotalVisitorsLabels       template.JS
			TotalVisitorsDps          template.JS
			SessionsDps               template.JS
			BouncesDps                template.JS
			PageVisitors              []tracking.PageVisitors
			PageRank                  []tracking.PageVisitors
			Languages                 []pirsch.LanguageStats
			Referrer                  []pirsch.ReferrerStats
			Browser                   []pirsch.BrowserStats
			OS                        []pirsch.OSStats
			Countries                 []pirsch.CountryStats
			Platform                  *pirsch.VisitorStats
			TimeOfDay                 []pirsch.TimeOfDayVisitors
			TimeOfDayMax              float64
			HourlyVisitorsTodayLabels template.JS
			HourlyVisitorsTodayDps    template.JS
			ActiveVisitors            int
			ActiveVisitorPages        []pirsch.Stats
		}{
			start,
			startDate,
			endDate,
			totalVisitorsLabels,
			totalVisitorsDps,
			sessionsDps,
			bouncesDps,
			pageVisitors,
			pageRank,
			tracking.GetLanguages(startDate, endDate),
			tracking.GetReferrer(startDate, endDate),
			tracking.GetBrowser(startDate, endDate),
			tracking.GetOS(startDate, endDate),
			tracking.GetCountry(startDate, endDate),
			tracking.GetPlatform(startDate, endDate),
			timeOfDay,
			float64(timeOfDayMax),
			hourlyVisitorsTodayLabels,
			hourlyVisitorsTodayDps,
			activeVisitors,
			activeVisitorPages,
		})
	}
}

func serveNotFound() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tplCache.Render(w, "notfound.html", nil)
	}
}

func setupRouter() *mux.Router {
	router := mux.NewRouter()
	router.PathPrefix(staticDirPrefix).Handler(http.StripPrefix(staticDirPrefix, gziphandler.GzipHandler(http.FileServer(http.Dir(staticDir)))))
	router.Handle("/blog/{slug}", serveBlogArticle())
	router.Handle("/blog", serveBlogPage())
	router.Handle("/legal", serveLegal())
	router.Handle("/tracking", serveTracking())
	router.Handle("/", serveAbout())
	router.NotFoundHandler = serveNotFound()
	return router
}

func configureLog() {
	logbuch.SetFormatter(logbuch.NewFieldFormatter(logTimeFormat, "\t\t"))
	logbuch.Info("Configure logging...")
	level := strings.ToLower(os.Getenv("MB_LOGLEVEL"))

	if level == "debug" {
		logbuch.SetLevel(logbuch.LevelDebug)
	} else if level == "info" {
		logbuch.SetLevel(logbuch.LevelInfo)
	} else {
		logbuch.SetLevel(logbuch.LevelWarning)
	}
}

func logEnvConfig() {
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, envPrefix) {
			pair := strings.Split(e, "=")
			logbuch.Info(pair[0] + "=" + pair[1])
		}
	}
}

func configureCors(router *mux.Router) http.Handler {
	logbuch.Info("Configuring CORS...")

	origins := strings.Split(os.Getenv("MB_ALLOWED_ORIGINS"), ",")
	c := cors.New(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
		Debug:            strings.ToLower(os.Getenv("MB_CORS_LOGLEVEL")) == "debug",
	})
	return c.Handler(router)
}

func start(handler http.Handler, trackingCancel context.CancelFunc) {
	logbuch.Info("Starting server...")
	var server http.Server
	server.Handler = handler
	server.Addr = os.Getenv("MB_HOST")

	go func() {
		sigint := make(chan os.Signal)
		signal.Notify(sigint, os.Interrupt)
		<-sigint
		logbuch.Info("Shutting down server...")
		trackingCancel()
		tracker.Stop()
		ctx, _ := context.WithTimeout(context.Background(), shutdownTimeout)

		if err := server.Shutdown(ctx); err != nil {
			logbuch.Fatal("Error shutting down server gracefully", logbuch.Fields{"err": err})
		}
	}()

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logbuch.Fatal("Error starting server", logbuch.Fields{"err": err})
	}

	logbuch.Info("Server shut down")
}

func main() {
	configureLog()
	logEnvConfig()
	db.Migrate()
	var trackingCancel context.CancelFunc
	tracker, trackingCancel = tracking.NewTracker()
	tplCache = tpl.NewCache()
	blogInstance = blog.NewBlog(tplCache)
	router := setupRouter()
	corsConfig := configureCors(router)
	start(corsConfig, trackingCancel)
}

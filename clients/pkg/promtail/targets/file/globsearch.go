package file

import (
	"github.com/bmatcuk/doublestar"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"math/rand"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

type GlobSearcher struct {
	inProcess sync.Map
	result    sync.Map
	queue     chan taskInfo
	logger    log.Logger
}

func NewGlobSearcher(logger log.Logger) *GlobSearcher {
	gs := &GlobSearcher{
		inProcess: sync.Map{},
		result:    sync.Map{},
		queue:     make(chan taskInfo),
		logger:    logger,
	}
	go gs.searchTask()
	return gs
}

type searchResult struct {
	matches []string
	err     error
}

type taskInfo struct {
	Path         string
	ExcludePath  []string
	SuffixFilter []string
}

func (s *GlobSearcher) Search(path string, ExcludePath []string, SuffixFilter []string) ([]string, error) {
	if _, ok := s.inProcess.Load(path); !ok {
		level.Debug(s.logger).Log("notInProcess", path, "operator", "add to task queue")
		s.inProcess.Store(path, taskInfo{
			ExcludePath:  ExcludePath,
			SuffixFilter: SuffixFilter,
		})
		s.queue <- taskInfo{
			Path:         path,
			ExcludePath:  ExcludePath,
			SuffixFilter: SuffixFilter,
		}
	}
	mr, ok := s.result.Load(path)
	if !ok {
		return []string{}, nil
	}
	return mr.(*searchResult).matches, mr.(*searchResult).err
}

func (s *GlobSearcher) searchTask() {
	for {
		select {
		case taskInfo := <-s.queue:
			delay := rand.Intn(3000)
			time.Sleep(time.Millisecond * time.Duration(delay))
			level.Debug(s.logger).Log("searchTask", taskInfo.Path)
			mr := &searchResult{}
			mr.matches, mr.err = doublestar.Glob(taskInfo.Path)
			mr.matches, mr.err = s.dropExcludedPath(mr.matches, taskInfo.ExcludePath)
			mr.matches = s.filterSuffix(mr.matches, taskInfo.SuffixFilter)
			mr.matches = s.dropNoUpdatePath(mr.matches, time.Minute*30)
			s.result.Store(taskInfo.Path, mr)
			s.inProcess.Delete(taskInfo.Path)
		}
	}
}

func (s *GlobSearcher) dropNoUpdatePath(matched []string, period time.Duration) []string {
	newMatches := make([]string, 0)
	for _, p := range matched {
		fInfo, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fInfo.ModTime().Add(period).After(time.Now()) {
			newMatches = append(newMatches, p)
		}
	}
	return newMatches
}

func (s *GlobSearcher) dropExcludedPath(matches, excludePath []string) ([]string, error) {
	//level.Debug(t.logger).Log("func", "dropExcludedPath", "targetPath", t.path, "start time", time.Now().String())
	//needExclude := make(map[string]string)
	afterExcludeMatches := make([]string, 0)
	for _, m := range matches {
		keep := true
		for _, ep := range excludePath {
			if matched, err := path.Match(ep, m); err == nil && matched {
				keep = false
				break
			}
		}
		if keep {
			afterExcludeMatches = append(afterExcludeMatches, m)
		}
	}

	//for _, ep := range t.excludePath {
	//	ms, err := doublestar.Glob(ep)
	//	if err != nil {
	//		return nil, errors.Wrap(err, "filetarget.sync.excludePath.Glob")
	//	}
	//	for _, p := range ms {
	//		needExclude[p] = "ok"
	//	}
	//}
	//level.Info(t.logger).Log("func", "dropExcludedPath", "targetPath", t.path, "start time", time.Now().String())
	//
	//finalMatchs := make([]string, 0)
	//for _, m := range matches {
	//	if _, ok := needExclude[m]; !ok {
	//		finalMatchs = append(finalMatchs, m)
	//	}
	//}
	//level.Debug(t.logger).Log("func", "dropExcludedPath", "targetPath", t.path, "end time", time.Now().String())
	return afterExcludeMatches, nil
}

func (s *GlobSearcher) filterSuffix(matches []string, suffixFilter []string) []string {
	//level.Debug(s.logger).Log("filterSuffix", "targetPath", t.path, "start time", time.Now().String())
	if suffixFilter == nil || len(suffixFilter) == 0 {
		return matches
	}
	filteredPaths := make([]string, 0)

	for _, p := range matches {
		for _, suffix := range suffixFilter {
			if strings.HasSuffix(p, suffix) {
				filteredPaths = append(filteredPaths, p)
				break
			}
		}
	}
	//level.Debug(s.logger).Log("filterSuffix", "targetPath", t.path, "start time", time.Now().String())
	return filteredPaths
}

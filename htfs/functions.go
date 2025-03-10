package htfs

import (
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/robocorp/rcc/anywork"
	"github.com/robocorp/rcc/common"
	"github.com/robocorp/rcc/fail"
	"github.com/robocorp/rcc/pathlib"
	"github.com/robocorp/rcc/trollhash"
)

func JustFileExistCheck(library MutableLibrary, path, name, digest string) anywork.Work {
	return func() {
		location := library.ExactLocation(digest)
		if !pathlib.IsFile(location) {
			fullpath := filepath.Join(path, name)
			panic(fmt.Errorf("Content for %q [%s] is missing!", fullpath, digest))
		}
	}
}

func CatalogCheck(library MutableLibrary, fs *Root) Treetop {
	var tool Treetop
	tool = func(path string, it *Dir) error {
		for name, file := range it.Files {
			anywork.Backlog(JustFileExistCheck(library, path, name, file.Digest))
		}
		for name, subdir := range it.Dirs {
			err := tool(filepath.Join(path, name), subdir)
			if err != nil {
				return err
			}
		}
		return nil
	}
	return tool
}

func DigestMapper(target map[string]string) Treetop {
	var tool Treetop
	tool = func(path string, it *Dir) error {
		for name, subdir := range it.Dirs {
			tool(filepath.Join(path, name), subdir)
		}
		for name, file := range it.Files {
			target[file.Digest] = filepath.Join(path, name)
		}
		return nil
	}
	return tool
}

func DigestRecorder(target map[string]string) Treetop {
	var tool Treetop
	tool = func(path string, it *Dir) error {
		for name, subdir := range it.Dirs {
			tool(filepath.Join(path, name), subdir)
		}
		for name, file := range it.Files {
			target[filepath.Join(path, name)] = file.Digest
		}
		return nil
	}
	return tool
}

func IntegrityCheck(result map[string]string) Treetop {
	var tool Treetop
	tool = func(path string, it *Dir) error {
		for name, subdir := range it.Dirs {
			tool(filepath.Join(path, name), subdir)
		}
		for name, file := range it.Files {
			if file.Name != file.Digest {
				result[filepath.Join(path, name)] = file.Digest
			}
		}
		return nil
	}
	return tool
}

func Hasher(known map[string]map[string]bool) Filetask {
	return func(fullpath string, details *File) anywork.Work {
		return func() {
			_, ok := known[details.Name]
			if !ok {
				defer anywork.Backlog(RemoveFile(fullpath))
			}
			source, err := os.Open(fullpath)
			if err != nil {
				panic(fmt.Sprintf("Open %q, reason: %v", fullpath, err))
			}
			defer source.Close()

			var reader io.ReadCloser
			reader, err = gzip.NewReader(source)
			if err != nil {
				_, err = source.Seek(0, 0)
				fail.On(err != nil, "Failed to seek %q -> %v", fullpath, err)
				reader = source
			}
			digest := sha256.New()
			_, err = io.Copy(digest, reader)
			if err != nil {
				panic(fmt.Sprintf("Copy %q, reason: %v", fullpath, err))
			}
			details.Digest = fmt.Sprintf("%02x", digest.Sum(nil))
		}
	}
}

func Locator(seek string) Filetask {
	return func(fullpath string, details *File) anywork.Work {
		return func() {
			source, err := os.Open(fullpath)
			if err != nil {
				panic(fmt.Sprintf("Open %q, reason: %v", fullpath, err))
			}
			defer source.Close()
			digest := sha256.New()
			locator := trollhash.LocateWriter(digest, seek)
			_, err = io.Copy(locator, source)
			if err != nil {
				panic(fmt.Sprintf("Copy %q, reason: %v", fullpath, err))
			}
			details.Rewrite = locator.Locations()
			details.Digest = fmt.Sprintf("%02x", digest.Sum(nil))
		}
	}
}

func MakeBranches(path string, it *Dir) error {
	for _, subdir := range it.Dirs {
		err := MakeBranches(filepath.Join(path, subdir.Name), subdir)
		if err != nil {
			return err
		}
	}
	if len(it.Dirs) == 0 {
		err := os.MkdirAll(path, 0o750)
		if err != nil {
			return err
		}
	}
	return os.Chtimes(path, motherTime, motherTime)
}

func ScheduleLifters(library MutableLibrary, stats *stats) Treetop {
	var scheduler Treetop
	seen := make(map[string]bool)
	scheduler = func(path string, it *Dir) error {
		for name, subdir := range it.Dirs {
			scheduler(filepath.Join(path, name), subdir)
		}
		for name, file := range it.Files {
			if seen[file.Digest] {
				common.Trace("LiftFile %s %q already scheduled.", file.Digest, name)
				continue
			}
			seen[file.Digest] = true
			directory := library.Location(file.Digest)
			if !seen[directory] && !pathlib.IsDir(directory) {
				os.MkdirAll(directory, 0o755)
			}
			seen[directory] = true
			sinkpath := filepath.Join(directory, file.Digest)
			ok := pathlib.IsFile(sinkpath)
			stats.Dirty(!ok)
			if ok {
				continue
			}
			sourcepath := filepath.Join(path, name)
			anywork.Backlog(LiftFile(sourcepath, sinkpath))
		}
		return nil
	}
	return scheduler
}

func TryRemove(context, target string) (err error) {
	for delay := 0; delay < 5; delay += 1 {
		time.Sleep(time.Duration(delay*100) * time.Millisecond)
		err = os.Remove(target)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("Remove failure [%s, %s, %s], reason: %s", context, common.ControllerIdentity(), common.HolotreeSpace, err)
}

func TryRemoveAll(context, target string) (err error) {
	for delay := 0; delay < 5; delay += 1 {
		time.Sleep(time.Duration(delay*100) * time.Millisecond)
		err = os.RemoveAll(target)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("RemoveAll failure [%s, %s, %s], reason: %s", context, common.ControllerIdentity(), common.HolotreeSpace, err)
}

func TryRename(context, source, target string) (err error) {
	for delay := 0; delay < 5; delay += 1 {
		time.Sleep(time.Duration(delay*100) * time.Millisecond)
		err = os.Rename(source, target)
		if err == nil {
			return nil
		}
	}
	common.Debug("Heads up: rename about to fail [%q -> %q], reason: %s", source, target, err)
	origin := "source"
	intermediate := fmt.Sprintf("%s.%d_%x", source, os.Getpid(), rand.Intn(4096))
	err = os.Rename(source, intermediate)
	if err == nil {
		source = intermediate
		origin = "target"
	}
	for delay := 0; delay < 5; delay += 1 {
		time.Sleep(time.Duration(delay*100) * time.Millisecond)
		err = os.Rename(source, target)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("Rename failure [%s, %s, %s, %s], reason: %s", context, common.ControllerIdentity(), common.HolotreeSpace, origin, err)
}

func LiftFile(sourcename, sinkname string) anywork.Work {
	return func() {
		source, err := os.Open(sourcename)
		anywork.OnErrPanicCloseAll(err)

		defer source.Close()
		partname := fmt.Sprintf("%s.part%s", sinkname, <-common.Identities)
		defer os.Remove(partname)
		sink, err := os.Create(partname)
		anywork.OnErrPanicCloseAll(err)

		defer sink.Close()
		writer, err := gzip.NewWriterLevel(sink, gzip.BestSpeed)
		anywork.OnErrPanicCloseAll(err, sink)

		_, err = io.Copy(writer, source)
		anywork.OnErrPanicCloseAll(err, sink)

		anywork.OnErrPanicCloseAll(writer.Close(), sink)

		anywork.OnErrPanicCloseAll(sink.Close())

		runtime.Gosched()

		anywork.OnErrPanicCloseAll(TryRename("liftfile", partname, sinkname))
	}
}

func DropFile(library Library, digest, sinkname string, details *File, rewrite []byte) anywork.Work {
	return func() {
		reader, closer, err := library.Open(digest)
		anywork.OnErrPanicCloseAll(err)

		defer closer()
		partname := fmt.Sprintf("%s.part%s", sinkname, <-common.Identities)
		defer os.Remove(partname)
		sink, err := os.Create(partname)
		anywork.OnErrPanicCloseAll(err)

		_, err = io.Copy(sink, reader)
		anywork.OnErrPanicCloseAll(err, sink)

		for _, position := range details.Rewrite {
			_, err = sink.Seek(position, 0)
			if err != nil {
				sink.Close()
				panic(fmt.Sprintf("%v %d", err, position))
			}
			_, err = sink.Write(rewrite)
			anywork.OnErrPanicCloseAll(err, sink)
		}

		anywork.OnErrPanicCloseAll(sink.Close())

		anywork.OnErrPanicCloseAll(TryRename("dropfile", partname, sinkname))

		anywork.OnErrPanicCloseAll(os.Chmod(sinkname, details.Mode))
		anywork.OnErrPanicCloseAll(os.Chtimes(sinkname, motherTime, motherTime))
	}
}

func RemoveFile(filename string) anywork.Work {
	return func() {
		anywork.OnErrPanicCloseAll(TryRemove("file", filename))
	}
}

func RemoveDirectory(dirname string) anywork.Work {
	return func() {
		anywork.OnErrPanicCloseAll(TryRemoveAll("directory", dirname))
	}
}

func RestoreDirectory(library Library, fs *Root, current map[string]string, stats *stats) Dirtask {
	return func(path string, it *Dir) anywork.Work {
		return func() {
			content, err := os.ReadDir(path)
			anywork.OnErrPanicCloseAll(err)
			files := make(map[string]bool)
			for _, part := range content {
				directpath := filepath.Join(path, part.Name())
				if part.IsDir() {
					_, ok := it.Dirs[part.Name()]
					if !ok {
						common.Trace("* Holotree: remove extra directory %q", directpath)
						anywork.Backlog(RemoveDirectory(directpath))
					}
					stats.Dirty(!ok)
					continue
				}
				files[part.Name()] = true
				found, ok := it.Files[part.Name()]
				if !ok {
					common.Trace("* Holotree: remove extra file      %q", directpath)
					anywork.Backlog(RemoveFile(directpath))
					stats.Dirty(true)
					continue
				}
				shadow, ok := current[directpath]
				golden := !ok || found.Digest == shadow
				info, err := part.Info()
				anywork.OnErrPanicCloseAll(err)
				ok = golden && found.Match(info)
				stats.Dirty(!ok)
				if !ok {
					common.Trace("* Holotree: update changed file    %q", directpath)
					anywork.Backlog(DropFile(library, found.Digest, directpath, found, fs.Rewrite()))
				}
			}
			for name, found := range it.Files {
				directpath := filepath.Join(path, name)
				_, seen := files[name]
				if !seen {
					stats.Dirty(true)
					common.Trace("* Holotree: add missing file       %q", directpath)
					anywork.Backlog(DropFile(library, found.Digest, directpath, found, fs.Rewrite()))
				}
			}
		}
	}
}

type Zipper interface {
	Add(fullpath, relativepath string) error
}

func ZipRoot(library MutableLibrary, fs *Root, sink Zipper) Treetop {
	var tool Treetop
	baseline := common.HololibLocation()
	tool = func(path string, it *Dir) (err error) {
		defer fail.Around(&err)

		for _, file := range it.Files {
			location := library.ExactLocation(file.Digest)
			relative, err := filepath.Rel(baseline, location)
			fail.On(err != nil, "Relative path error: %s -> %s -> %v", baseline, location, err)
			err = sink.Add(location, relative)
			fail.On(err != nil, "%v", err)
		}
		for name, subdir := range it.Dirs {
			err := tool(filepath.Join(path, name), subdir)
			fail.On(err != nil, "%v", err)
		}
		return nil
	}
	return tool
}

func LoadHololibHashes() map[string]map[string]bool {
	catalogs, roots := LoadCatalogs()
	slots := make([]map[string]string, len(roots))
	for at, root := range roots {
		anywork.Backlog(DigestLoader(root, at, slots))
	}
	result := make(map[string]map[string]bool)
	runtime.Gosched()
	anywork.Sync()
	for at, slot := range slots {
		catalog := catalogs[at]
		for k, _ := range slot {
			found, ok := result[k]
			if !ok {
				found = make(map[string]bool)
				result[k] = found
			}
			found[catalog] = true
		}
	}
	return result
}

func DigestLoader(root *Root, at int, slots []map[string]string) anywork.Work {
	return func() {
		collector := make(map[string]string)
		task := DigestMapper(collector)
		err := task(root.Path, root.Tree)
		if err != nil {
			panic(fmt.Sprintf("Collecting dir %q, reason: %v", root.Path, err))
		}
		slots[at] = collector
		common.Trace("Root %q loaded.", root.Path)
	}
}

func LoadCatalogs() ([]string, []*Root) {
	common.TimelineBegin("catalog load start")
	defer common.TimelineEnd()
	catalogs := Catalogs()
	roots := make([]*Root, len(catalogs))
	for at, catalog := range catalogs {
		fullpath := filepath.Join(common.HololibCatalogLocation(), catalog)
		anywork.Backlog(CatalogLoader(fullpath, at, roots))
		catalogs[at] = fullpath
	}
	runtime.Gosched()
	anywork.Sync()
	return catalogs, roots
}

func CatalogLoader(catalog string, at int, roots []*Root) anywork.Work {
	return func() {
		tempdir := filepath.Join(common.RobocorpTemp(), "shadow")
		shadow, err := NewRoot(tempdir)
		if err != nil {
			panic(fmt.Sprintf("Temp dir %q, reason: %v", tempdir, err))
		}
		err = shadow.LoadFrom(catalog)
		if err != nil {
			panic(fmt.Sprintf("Load %q, reason: %v", catalog, err))
		}
		roots[at] = shadow
		common.Trace("Catalog %q loaded.", catalog)
	}
}

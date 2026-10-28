package actions

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

// Rejected is a file that failed a check. Its message says what is wrong
// with the file, and is safe to show to the partner who sent it.
type Rejected struct{ Reason string }

func (e *Rejected) Error() string { return e.Reason }

// Rejected marks the error for packages that can't import this one.
func (e *Rejected) Rejected() bool { return true }

func reject(format string, a ...any) error {
	return &Rejected{Reason: fmt.Sprintf(format, a...)}
}

// checkFile rejects the file at path if it breaks one of the check's rules.
func checkFile(path string, c *config.Check) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	size := info.Size()
	if c.MinSize > 0 && size < int64(c.MinSize) {
		return reject("file is %d bytes, below the minimum of %d", size, int64(c.MinSize))
	}
	if c.MaxSize > 0 && size > int64(c.MaxSize) {
		return reject("file is %d bytes, above the maximum of %d", size, int64(c.MaxSize))
	}
	if c.CSV != nil {
		return checkCSV(path, c.CSV)
	}
	return nil
}

// checkCSV reads the whole file as CSV. The first row is the header; every
// row must have as many columns as it.
func checkCSV(path string, c *config.CSVCheck) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer closeQuietly(f)

	rd := csv.NewReader(bufio.NewReader(f))
	rd.ReuseRecord = true
	if c.Delimiter != "" {
		rd.Comma, _ = utf8.DecodeRuneInString(c.Delimiter)
	}

	header, err := rd.Read()
	if errors.Is(err, io.EOF) {
		return reject("file is empty, expected CSV")
	}
	if err != nil {
		return reject("not valid CSV: %v", err)
	}
	// Spreadsheets often start the file with a byte-order mark.
	header[0] = strings.TrimPrefix(header[0], "\ufeff")
	if len(c.Header) > 0 && !slices.Equal(header, c.Header) {
		return reject("CSV header is %q, expected %q", header, c.Header)
	}

	rows := 0
	for {
		_, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return reject("not valid CSV: %v", err)
		}
		rows++
	}
	if rows < c.MinRows {
		return reject("CSV has %d rows after the header, expected at least %d", rows, c.MinRows)
	}
	return nil
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/otiai10/copy"
	"github.com/spf13/cobra"
)

func main() {
	cmd := NewRootCmd(os.Args[1:])
	if err := cmd.Execute(); err != nil {
		log.Fatal("Failed to execute command")
	}
}

// NewRootCmd represents the base command when called without any subcommands
func NewRootCmd(args []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inject",
		Short: "",
		Long:  ``,
	}

	out := cmd.OutOrStdout()

	cmd.AddCommand(NewUpgradeCommand(out))

	return cmd
}

type upgradeCmd struct {
	injector    string
	command     string
	injectFlags []string
	release     string
	chart       string
	dryRun      bool
	debug       bool
	valueFiles  []string
	values      []string
	install     bool
	namespace   string
	kubeContext string
	timeout     int

	tls     bool
	tlsCert string
	tlsKey  string

	resetValues bool
	force       bool

	out io.Writer
}

// NewUpgradeCommand represents the upgrade command
func NewUpgradeCommand(out io.Writer) *cobra.Command {
	u := &upgradeCmd{out: out}

	cmd := &cobra.Command{
		Use:   "upgrade [RELEASE] [CHART]",
		Short: "upgrade a release including inject",
		Long:  ``,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return errors.New("requires two arguments")
			}
			if u.injector == "helm" {
				return errors.New("why you do this to me")
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			release := args[0]
			chart := args[1]

			tempDir, err := copyToTempDir(chart)
			if err != nil {
				fmt.Println(err)
				return
			}
			// defer os.RemoveAll(tempDir)

			fileOptions := fileOptions{
				basePath:     tempDir,
				matchSubPath: "templates",
				fileType:     "yaml",
			}
			files, err := getFilesToActOn(fileOptions)
			if err != nil {
				fmt.Println(err)
				return
			}

			templateOptions := templateOptions{
				files:       files,
				chart:       tempDir,
				name:        release,
				namespace:   u.namespace,
				values:      u.values,
				valuesFiles: u.valueFiles,
			}
			if err := template(templateOptions); err != nil {
				fmt.Println(err)
				return
			}

			injectOptions := injectOptions{
				injector:    u.injector,
				command:     u.command,
				injectFlags: u.injectFlags,
				files:       files,
			}
			if err := inject(injectOptions); err != nil {
				fmt.Println(err)
				return
			}

			upgradeOptions := upgradeOptions{
				chart:       tempDir,
				name:        release,
				values:      u.values,
				valuesFiles: u.valueFiles,
				namespace:   u.namespace,
				kubeContext: u.kubeContext,
				timeout:     u.timeout,
				install:     u.install,
				dryRun:      u.dryRun,
				debug:       u.debug,
				tls:         u.tls,
				tlsCert:     u.tlsCert,
				tlsKey:      u.tlsKey,
				resetValues: u.resetValues,
				force:       u.force,
			}
			if err := upgrade(upgradeOptions); err != nil {
				fmt.Println(err)
				return
			}
		},
	}
	f := cmd.Flags()

	f.StringVar(&u.injector, "injector", "linkerd", "injector to use (must be pre-installed)")
	f.StringVar(&u.command, "command", "inject", "injection command to be used")
	f.StringSliceVar(&u.injectFlags, "inject-flags", []string{}, "flags to be passed to injector, without leading \"--\" (can specify multiple). Example: \"--inject-flags tls=optional,skip-inbound-ports=25,skip-inbound-ports=26\"")

	f.StringArrayVarP(&u.valueFiles, "values", "f", []string{}, "specify values in a YAML file or a URL (can specify multiple)")
	f.StringArrayVar(&u.values, "set", []string{}, "set values on the command line (can specify multiple)")
	f.StringVar(&u.namespace, "namespace", "", "namespace to install the release into (only used if --install is set). Defaults to the current kube config namespace")
	f.StringVar(&u.kubeContext, "kubecontext", "", "name of the kubeconfig context to use")
	f.IntVar(&u.timeout, "timeout", 300, "time in seconds to wait for any individual Kubernetes operation (like Jobs for hooks)")

	f.BoolVarP(&u.install, "install", "i", false, "if a release by this name doesn't already exist, run an install")
	f.BoolVar(&u.dryRun, "dry-run", false, "simulate an upgrade")
	f.BoolVar(&u.debug, "debug", false, "enable verbose output")

	f.BoolVar(&u.tls, "tls", false, "enable TLS for request")
	f.StringVar(&u.tlsCert, "tls-cert", "", "path to TLS certificate file (default: $HELM_HOME/cert.pem)")
	f.StringVar(&u.tlsKey, "tls-key", "", "path to TLS key file (default: $HELM_HOME/key.pem)")

	f.BoolVar(&u.resetValues, "reset-values", false, "When upgrading, reset the values to the ones built into the chart")
	f.BoolVar(&u.force, "force", false, "Force resource update through delete/recreate if needed")

	return cmd
}

// copyToTempDir checks if the path is local or a repo (in this order) and copies it to a temp directory
// It will perform a `helm fetch` if required
func copyToTempDir(path string) (string, error) {
	tempDir := mkRandomDir(os.TempDir())
	exists, err := exists(path)
	if err != nil {
		return "", err
	}
	if !exists {
		command := fmt.Sprintf("helm fetch %s --untar -d %s", path, tempDir)
		_, stderr, err := Exec(command)
		if err != nil || len(stderr) != 0 {
			return "", fmt.Errorf(string(stderr))
		}
		files, err := ioutil.ReadDir(tempDir)
		if err != nil {
			return "", err
		}
		if len(files) != 1 {
			return "", fmt.Errorf("%d additional files found in temp direcotry. This is very strange", len(files)-1)
		}
		tempDir = filepath.Join(tempDir, files[0].Name())
	} else {
		err = copy.Copy(path, tempDir)
		if err != nil {
			return "", err
		}
	}
	return tempDir, nil
}

type fileOptions struct {
	basePath     string
	matchSubPath string
	fileType     string
}

// getFilesToActOn returns a slice of files that are within the base path, has a matching sub path and file type
func getFilesToActOn(o fileOptions) ([]string, error) {
	var files []string

	err := filepath.Walk(o.basePath, func(path string, info os.FileInfo, err error) error {
		if !strings.Contains(path, o.matchSubPath+"/") {
			return nil
		}
		if !strings.HasSuffix(path, o.fileType) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return files, nil
}

type templateOptions struct {
	files       []string
	chart       string
	name        string
	values      []string
	valuesFiles []string
	namespace   string
}

func template(o templateOptions) error {
	var additionalFlags string
	additionalFlags += createFlagChain("set", o.values)
	defaultValuesPath := filepath.Join(o.chart, "values.yaml")
	exists, err := exists(defaultValuesPath)
	if err != nil {
		return err
	}
	if exists {
		additionalFlags += createFlagChain("f", []string{defaultValuesPath})
	}
	additionalFlags += createFlagChain("f", o.valuesFiles)
	if o.namespace != "" {
		additionalFlags += createFlagChain("namespace", []string{o.namespace})
	}

	for _, file := range o.files {
		command := fmt.Sprintf("helm template --debug=false %s --name %s -x %s%s", o.chart, o.name, file, additionalFlags)
		stdout, stderr, err := Exec(command)
		if err != nil || len(stderr) != 0 {
			return fmt.Errorf(string(stderr))
		}
		// Prevent duplicate rendering
		if err := ioutil.WriteFile(file+".tmp", stdout, 0644); err != nil {
			return err
		}
	}
	for _, file := range o.files {
		if err := os.Rename(file+".tmp", file); err != nil {
			return err
		}
	}

	return nil
}

type injectOptions struct {
	injector    string
	command     string
	injectFlags []string
	files       []string
}

func inject(o injectOptions) error {
	var flags string
	for _, flag := range o.injectFlags {
		flagSplit := strings.Split(flag, "=")
		if len(flagSplit) != 2 {
			return fmt.Errorf("inject-flags must be in the form of key1=value1[,key2=value2,...]")
		}
		key, val := flagSplit[0], flagSplit[1]
		flags += createFlagChain(key, []string{val})
	}
	for _, file := range o.files {
		command := fmt.Sprintf("%s %s%s %s", o.injector, o.command, flags, file)
		stdout, stderr, err := Exec(command)
		if err != nil {
			return fmt.Errorf(string(stderr))
		}
		if err := ioutil.WriteFile(file, stdout, 0644); err != nil {
			return err
		}
	}

	return nil
}

type upgradeOptions struct {
	chart       string
	name        string
	values      []string
	valuesFiles []string
	namespace   string
	kubeContext string
	timeout     int
	install     bool
	dryRun      bool
	debug       bool
	tls         bool
	tlsCert     string
	tlsKey      string
	kubeConfig  string
	resetValues bool
	force       bool
}

func upgrade(o upgradeOptions) error {
	var additionalFlags string
	additionalFlags += createFlagChain("set", o.values)
	additionalFlags += createFlagChain("f", o.valuesFiles)
	additionalFlags += createFlagChain("timeout", []string{fmt.Sprintf("%d", o.timeout)})
	if o.namespace != "" {
		additionalFlags += createFlagChain("namespace", []string{o.namespace})
	}
	if o.kubeContext != "" {
		additionalFlags += createFlagChain("kube-context", []string{o.kubeContext})
	}
	if o.install {
		additionalFlags += createFlagChain("i", []string{""})
	}
	if o.dryRun {
		additionalFlags += createFlagChain("dry-run", []string{""})
	}
	if o.debug {
		additionalFlags += createFlagChain("debug", []string{""})
	}
	if o.tls {
		additionalFlags += createFlagChain("tls", []string{""})
	}
	if o.tlsCert != "" {
		additionalFlags += createFlagChain("tls-cert", []string{o.tlsCert})
	}
	if o.tlsKey != "" {
		additionalFlags += createFlagChain("tls-key", []string{o.tlsKey})
	}
	if o.resetValues {
		additionalFlags += createFlagChain("reset-values", []string{""})
	}
	if o.force {
		additionalFlags += createFlagChain("force", []string{""})
	}

	command := fmt.Sprintf("helm upgrade %s %s%s", o.name, o.chart, additionalFlags)
	stdout, stderr, err := Exec(command)
	if err != nil || len(stderr) != 0 {
		return fmt.Errorf(string(stderr))
	}
	fmt.Println(string(stdout))

	return nil
}

// Exec takes a command as a string and executes it
func Exec(cmd string) ([]byte, []byte, error) {
	args := strings.Split(cmd, " ")
	binary := args[0]
	_, err := exec.LookPath(binary)
	if err != nil {
		return nil, nil, err
	}

	command := exec.Command(binary, args[1:]...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if err != nil {
		log.Print(stderr.String())
		log.Fatal(err)
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

// MkRandomDir creates a new directory with a random name made of numbers
func mkRandomDir(basepath string) string {
	r := strconv.Itoa((rand.New(rand.NewSource(time.Now().UnixNano()))).Int())
	path := filepath.Join(basepath, r)
	os.Mkdir(path, 0755)

	return path
}

func createFlagChain(flag string, input []string) string {
	chain := ""
	dashes := "--"
	if len(flag) == 1 {
		dashes = "-"
	}

	for _, i := range input {
		if i != "" {
			i = " " + i
		}
		chain = fmt.Sprintf("%s %s%s%s", chain, dashes, flag, i)
	}

	return chain
}

// exists returns whether the given file or directory exists or not
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return true, err
}

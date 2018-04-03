package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"text/template"

	"github.com/automationbroker/bundle-lib/apb"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

const dockerfileTemplate string = `FROM ansibleplaybookbundle/helm-bundle-base

LABEL "com.redhat.apb.spec"=\
""

COPY {{.TarfileName}} /opt/chart.tgz

ENTRYPOINT ["entrypoint.sh"]
`

const apbYml string = "apb.yml"
const dockerfile string = "Dockerfile"

// NewSpec returns a pointer to a new APB that has been populated with the
// passed-in data.
func NewSpec(v TarValues) *apb.Spec {
	parameter := apb.ParameterDescriptor{
		Name:        "values",
		Title:       "Values",
		Type:        "string",
		DisplayType: "textarea",
		Default:     v.Values,
	}
	plan := apb.Plan{
		Name:        "default",
		Description: fmt.Sprintf("Deploys helm chart %s", v.Name),
		Free:        true,
		Metadata:    make(map[string]interface{}),
		Parameters:  []apb.ParameterDescriptor{parameter},
	}
	spec := apb.Spec{
		Version:     "1.0",
		Name:        fmt.Sprintf("%s-apb", v.Name),
		Description: v.Description,
		Bindable:    false,
		Async:       "optional",
		Metadata: map[string]interface{}{
			"displayName": fmt.Sprintf("%s (helm bundle)", v.Name),
			"imageUrl":    v.Icon,
		},
		Plans: []apb.Plan{plan},
	}
	return &spec
}

// TarValues holds data that will be used to create the Dockerfile and apb.yml
type TarValues struct {
	Name        string
	Description string
	Icon        string
	TarfileName string
	Values      string // the entire contents of the chart's values.yaml file
}

// Chart holds data that is parsed from a helm chart's Chart.yaml file.
type Chart struct {
	Description string
	Name        string
	Icon        string
}

func main() {
	// forceArg is true when the user specifies --force, and it indicates that
	// it is ok to replace existing files.
	var forceArg bool

	var rootCmd = &cobra.Command{
		Use:   "helm2bundle CHARTFILE",
		Short: "Packages a helm chart as a Service Bundle",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			run(forceArg, args[0])
		},
	}

	rootCmd.PersistentFlags().BoolVarP(&forceArg, "force", "f", false, "force overwrite of existing files")

	err := rootCmd.Execute()
	if err != nil {
		fmt.Println(err.Error())
		fmt.Println("could not execute command")
		os.Exit(1)
	}
}

// run does all of the real work. `force` indicates if existing files should be
// overwritten, and `filename` is the name of the chart file in the working
// directory.
func run(force bool, filename string) {
	if force == false {
		// fail if one of the files already exists
		exists, err := fileExists()
		if err != nil {
			fmt.Println(err.Error())
			fmt.Println("could not get values from helm chart")
			os.Exit(1)
		}
		if exists {
			fmt.Printf("use --force to overwrite existing %s and/or %s\n", dockerfile, apbYml)
			os.Exit(1)
		}
	}

	values, err := getTarValues(filename)
	if err != nil {
		fmt.Println(err.Error())
		fmt.Println("could not get values from helm chart")
		os.Exit(1)
	}

	err = writeApbYaml(values)
	if err != nil {
		fmt.Println(err.Error())
		fmt.Println("could not render template")
		os.Exit(1)
	}
	err = writeDockerfile(values)
	if err != nil {
		fmt.Println(err.Error())
		fmt.Println("could not render template")
		os.Exit(1)
	}
}

// fileExists returns true if either apb.yml or Dockerfile exists in the
// working directory, else false
func fileExists() (bool, error) {
	for _, filename := range []string{apbYml, dockerfile} {
		_, err := os.Stat(filename)
		if err == nil {
			// file exists
			return true, nil
		}
		if !os.IsNotExist(err) {
			// error determining if file exists
			return false, err
		}
	}
	// neither file exists
	return false, nil
}

// writeApbYaml creates a new file named "apb.yml" in the current working
// directory that can be used to build a service bundle.
func writeApbYaml(v TarValues) error {
	apb := NewSpec(v)
	data, err := yaml.Marshal(apb)
	if err != nil {
		return err
	}

	f, err := os.Create(apbYml)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}

// writeDockerfile creates a new file named "Dockerfile" in the current working
// directory that can be used to build a service bundle.
func writeDockerfile(v TarValues) error {
	t, err := template.New(dockerfile).Parse(dockerfileTemplate)
	if err != nil {
		return err
	}

	f, err := os.Create(dockerfile)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, v)
}

// getTarValues opens the helm chart tarball to 1) retrieve Chart.yaml so it can
// be parsed, and 2) retrieve the entire contents of values.yaml.
func getTarValues(filename string) (TarValues, error) {
	file, err := os.Open(filename)
	if err != nil {
		return TarValues{}, err
	}
	defer file.Close()

	uncompressed, err := gzip.NewReader(file)
	if err != nil {
		return TarValues{}, err
	}

	tr := tar.NewReader(uncompressed)
	var chart Chart
	var values string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return TarValues{}, errors.New("Chart.yaml not found in archive")
		}
		if err != nil {
			return TarValues{}, err
		}

		chartMatch, err := path.Match("*/Chart.yaml", hdr.Name)
		if err != nil {
			return TarValues{}, err
		}
		valuesMatch, err := path.Match("*/values.yaml", hdr.Name)
		if err != nil {
			return TarValues{}, err
		}
		if chartMatch {
			chart, err = parseChart(tr)
			if err != nil {
				return TarValues{}, err
			}
		}
		if valuesMatch {
			data, err := ioutil.ReadAll(tr)
			if err != nil {
				return TarValues{}, err
			}
			values = string(data)
		}
		if len(values) > 0 && len(chart.Name) > 0 {
			break
		}
	}
	if len(values) > 0 && len(chart.Name) > 0 {
		return TarValues{
			Name:        chart.Name,
			Description: chart.Description,
			Icon:        chart.Icon,
			TarfileName: filename,
			Values:      values,
		}, nil
	}
	return TarValues{}, errors.New("Could not find both Chart.yaml and values.yaml")
}

// parseChart parses the Chart.yaml file for data that is needed when creating
// a service bundle.
func parseChart(source io.Reader) (Chart, error) {
	c := Chart{}

	data, err := ioutil.ReadAll(source)
	if err != nil {
		return c, err
	}

	err = yaml.Unmarshal(data, &c)
	if err != nil {
		return c, err
	}

	return c, nil
}

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

var (
	hub              = os.Getenv("HUB")
	basePathForBuild = "build"
)

func init() {
	if hub == "" {
		panic(errors.New("missing ${HUB}"))
	}
}

func cleanup() error {
	files, err := glob(".github/workflows/*", "sync/Dockerfile.*")
	if err != nil {
		return err
	}

	for i := range files {
		if err := os.Remove(files[i]); err != nil {
			return err
		}
	}

	return nil
}

func main() {
	if err := cleanup(); err != nil {
		panic(err)
	}

	projects, err := resolveProjects()
	if err != nil {
		panic(err)
	}

	data, _ := yaml.Marshal(projects)
	fmt.Println(string(data))

	generateWorkflows(projects)
	generateDependabot(projects)
}

func generateWorkflows(projects Projects) {
	projects.Range(func(p *Project) {
		for i := range p.Dockerfiles {
			name, tags := nameAndTagsFromDockerfile(p.Dockerfiles[i])

			w := &GithubWorkflow{}

			if p.Name == name {
				w.Name = p.Name
			} else {
				w.Name = p.Name + "-" + name
			}

			w.On = Values{
				"push": Values{
					"paths": []string{
						workflowFilename(w.Name),
						p.Dockerfiles[i],
						p.VersionFile,
						p.Makefile,
					},
				},
			}

			workingDir := filepath.Join(basePathForBuild, p.Name)

			steps := []*WorkflowStep{
				Step(StepUses("actions/checkout@v2")),
				Step(StepUses("docker/setup-qemu-action@v1")),
				Step(StepUses("docker/setup-buildx-action@v1")),
			}

			imageTags := make([]string, 0)

			for _, h := range strings.Split(hub, " ") {
				if h != "" {
					imageTags = append(imageTags, fmt.Sprintf(`%s/${{ steps.prepare.outputs.image }}`, h))

					registry := strings.Split(h, "/")[0]

					hubLogin := map[string]string{
						"registry": registry,
					}

					name := strings.ToUpper(strings.Split(registry, ".")[0])

					switch registry {
					case "ghcr.io":
						hubLogin["username"] = "${{ github.repository_owner }}"
						hubLogin["password"] = "${{ secrets.CR_PAT }}"
					default:
						hubLogin["username"] = "${{ secrets." + name + "_USERNAME }}"
						hubLogin["password"] = "${{ secrets." + name + "_PASSWORD }}"
					}

					steps = append(steps, Step(StepName("Login "+registry), StepUses("docker/login-action@v1"), StepWith(hubLogin)))
				}
			}

			steps = append(steps,
				Step(
					StepName("prepare"),
					StepID("prepare"),
					StepRun(`
if [[ ${{ github.ref }} == "refs/heads/master" ]]; then
  make prepare NAME=`+name+`
else
  make prepare NAME=`+name+` TAG=temp-${{ github.sha }}
fi 
`),
				),
				Step(
					StepName("Push"),
					StepUses("docker/build-push-action@v2"),
					StepWith(map[string]string{
						"context":    workingDir,
						"file":       workingDir + "/Dockerfile." + name,
						"push":       "${{ github.event_name != 'pull_request' }}",
						"build-args": "${{ steps.prepare.outputs.build_args }}",
						"labels": strings.Join([]string{
							"org.opencontainers.image.source=https://github.com/${{ github.repository }}",
							"org.opencontainers.image.revision=${{ github.sha }}",
						}, "\n"),
						"platforms": "linux/amd64,linux/arm64",
						"tags":      strings.Join(imageTags, "\n"),
					}),
				),
			)

			w.Jobs = map[string]*WorkflowJob{
				name: Job(
					JobDefaultsWorkingDirectory(workingDir),
					JobRunsOn(tags...),
					JobSteps(steps...),
				),
			}

			writeWorkflow(w)
		}
	})
}

func generateDependabot(projects Projects) {
	buf := bytes.NewBufferString(`
version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "daily"
`)

	projects.Range(func(p *Project) {
		_, _ = io.WriteString(buf, fmt.Sprintf(`
  - package-ecosystem: "docker"
    directory: "/build/%s"
    schedule:
      interval: "daily"
`, p.Name))
	})

	_ = generateFile(".github/dependabot.yml", buf.Bytes())
}

func writeWorkflow(w *GithubWorkflow) {
	if w == nil {
		return
	}
	data, _ := yaml.Marshal(w)
	_ = generateFile(workflowFilename(w.Name), data)
}

func workflowFilename(name string) string {
	return fmt.Sprintf(".github/workflows/%s.yml", name)
}

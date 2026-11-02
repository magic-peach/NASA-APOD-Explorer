// Jenkins pipeline mirroring .github/workflows/ci.yml: lint -> test -> build image.
// An alternative for teams that run Jenkins instead of GitHub Actions; see
// docs/INTEGRATIONS.md for the agent requirements.
//
// Requires: a Docker-capable agent and the "Docker Pipeline" plugin.

pipeline {
    agent none

    options {
        timeout(time: 20, unit: 'MINUTES')
        disableConcurrentBuilds()
        buildDiscarder(logRotator(numToKeepStr: '20'))
    }

    environment {
        IMAGE = "nasa-apod-explorer:${env.BUILD_NUMBER}"
        // Keep Go's caches inside the workspace so containers can write them.
        GOCACHE = "${env.WORKSPACE}/.cache/go-build"
        GOMODCACHE = "${env.WORKSPACE}/.cache/go-mod"
    }

    stages {
        stage('Lint') {
            agent {
                docker { image 'golangci/golangci-lint:v2.7.2'; reuseNode true }
            }
            steps {
                sh '''
                    unformatted=$(gofmt -l .)
                    if [ -n "$unformatted" ]; then
                        echo "These files need gofmt:"; echo "$unformatted"; exit 1
                    fi
                    go vet ./...
                    golangci-lint run
                '''
            }
        }

        stage('Test') {
            agent {
                // The Debian-based image includes gcc, which -race needs.
                docker { image 'golang:1.24'; reuseNode true }
            }
            steps {
                sh '''
                    go test -race -covermode=atomic -coverprofile=coverage.out ./...
                    go tool cover -func=coverage.out | tail -1
                '''
            }
            post {
                always {
                    archiveArtifacts artifacts: 'coverage.out', allowEmptyArchive: true
                }
            }
        }

        stage('Build image') {
            agent any
            steps {
                sh 'docker build -t "$IMAGE" .'
            }
        }
    }

    post {
        always {
            node('') {
                // Don't let images pile up on the agent.
                sh 'docker image rm -f "$IMAGE" || true'
            }
        }
    }
}

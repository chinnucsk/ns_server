package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
)

type TagHist []uint
type Engine struct {
	generator RIGenerator
}
type OutputFormat string

var availableGenerators []RIGenerator = []RIGenerator{
	GlpkRIGenerator{},
	DummyRIGenerator{},
}

var diag *log.Logger

var (
	seed         int64
	tagHistogram TagHist     = nil
	params       VbmapParams = VbmapParams{
		Tags: nil,
	}
	engine       Engine       = Engine{availableGenerators[0]}
	outputFormat OutputFormat = "text"
	diagTo       string       = "stderr"
)

func (tags *TagMap) Set(s string) error {
	*tags = make(TagMap)

	for _, pair := range strings.Split(s, ",") {
		tagNode := strings.Split(pair, ":")
		if len(tagNode) != 2 {
			return fmt.Errorf("invalid tag-node pair (%s)", pair)
		}

		node, err := strconv.ParseUint(tagNode[0], 10, strconv.IntSize)
		if err != nil {
			return err
		}

		tag, err := strconv.ParseUint(tagNode[1], 10, strconv.IntSize)
		if err != nil {
			return err
		}

		(*tags)[Node(node)] = Tag(tag)
	}
	return nil
}

func (hist *TagHist) Set(s string) error {
	values := strings.Split(s, ",")
	*hist = make(TagHist, len(values))

	for i, v := range values {
		count, err := strconv.ParseUint(v, 10, strconv.IntSize)
		if err != nil {
			return err
		}

		(*hist)[i] = uint(count)
	}

	return nil
}

func (hist TagHist) String() string {
	return fmt.Sprintf("%v", []uint(hist))
}

func (engine *Engine) Set(s string) error {
	for _, gen := range availableGenerators {
		if s == gen.String() {
			*engine = Engine{gen}
			return nil
		}
	}

	return fmt.Errorf("unknown engine")
}

func (engine Engine) String() string {
	return engine.generator.String()
}

func (format *OutputFormat) Set(s string) error {
	switch s {
	case "text", "json", "ext-json":
		*format = OutputFormat(s)
	default:
		return fmt.Errorf("unrecognized output format")
	}

	return nil
}

func (format OutputFormat) String() string {
	return string(format)
}

func normalizeParams(params *VbmapParams) {
	if params.NumReplicas+1 > params.NumNodes {
		params.NumReplicas = params.NumNodes - 1
	}

	if params.NumSlaves >= params.NumNodes {
		params.NumSlaves = params.NumNodes - 1
	}

	if params.NumSlaves < params.NumReplicas {
		params.NumReplicas = params.NumSlaves
	}
}

func checkInput() {
	if params.NumNodes <= 0 || params.NumSlaves <= 0 || params.NumVBuckets <= 0 {
		fatal("num-nodes, num-slaves and num-vbuckets must be greater than zero")
	}

	if params.NumReplicas < 0 {
		fatal("num-replicas must be greater of equal than zero")
	}

	if params.Tags != nil && tagHistogram != nil {
		fatal("Options --tags and --tag-histogram are exclusive")
	}

	normalizeParams(&params)

	if params.Tags == nil && tagHistogram == nil {
		diag.Printf("Tags are not specified. Assuming every node on a separate tag.")
		tagHistogram = make(TagHist, params.NumNodes)

		for i := 0; i < params.NumNodes; i++ {
			tagHistogram[i] = 1
		}
	}

	if tagHistogram != nil {
		tag := 0
		params.Tags = make(TagMap)

		for i := 0; i < params.NumNodes; i++ {
			for tag < len(tagHistogram) && tagHistogram[tag] == 0 {
				tag += 1
			}
			if tag >= len(tagHistogram) {
				fatal("Invalid tag histogram. Counts do not add up.")
			}

			tagHistogram[tag] -= 1
			params.Tags[Node(i)] = Tag(tag)
		}

		if tag != len(tagHistogram)-1 || tagHistogram[tag] != 0 {
			fatal("Invalid tag histogram. Counts do not add up.")
		}
	}

	// each node should have a tag assigned
	for i := 0; i < params.NumNodes; i++ {
		_, present := params.Tags[Node(i)]
		if !present {
			fatal("Tag for node %v not specified", i)
		}
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	// TODO
	flag.IntVar(&params.NumNodes, "num-nodes", 25, "number of nodes")
	flag.IntVar(&params.NumSlaves, "num-slaves", 10, "number of slaves")
	flag.IntVar(&params.NumVBuckets, "num-vbuckets", 1024, "number of VBuckets")
	flag.IntVar(&params.NumReplicas, "num-replicas", 1, "number of replicas")
	flag.Var(&params.Tags, "tags", "tags")
	flag.Var(&tagHistogram, "tag-histogram", "tag histogram")
	flag.Var(&engine, "engine", "engine used to generate the topology")
	flag.Var(&outputFormat, "output-format", "output format")
	flag.StringVar(&diagTo, "diag", "stderr", "where to send diagnostics")

	flag.Int64Var(&seed, "seed", time.Now().UTC().UnixNano(), "random seed")

	flag.Parse()

	var diagSink io.Writer
	switch diagTo {
	case "stderr":
		diagSink = os.Stderr
	case "null":
		diagSink = ioutil.Discard
	default:
		diagFile, err := os.Create(diagTo)
		if err != nil {
			fatal("Couldn't create diagnostics file: %s", err.Error())
		}
		defer func() {
			diagFile.Close()
		}()
		diagSink = diagFile
	}

	diag = log.New(diagSink, "", 0)
	diag.Printf("Started as:\n  %s", strings.Join(os.Args, " "))

	diag.Printf("Using %d as a seed", seed)
	rand.Seed(seed)

	checkInput()

	diag.Printf("Finalized parameters")
	diag.Printf("  Number of nodes: %d", params.NumNodes)
	diag.Printf("  Number of slaves: %d", params.NumSlaves)
	diag.Printf("  Number of vbuckets: %d", params.NumVBuckets)
	diag.Printf("  Number of replicas: %d", params.NumReplicas)
	diag.Printf("  Tags assignments:")

	for i := 0; i < params.NumNodes; i++ {
		diag.Printf("    %d -> %v", i, params.Tags[Node(i)])
	}

	start := time.Now()

	solution, err := VbmapGenerate(params, engine.generator)
	if err != nil {
		fatal("ERROR: %s", err.Error())
	}

	duration := time.Since(start)
	diag.Printf("Generated vbucket map in %s (wall clock)", duration.String())

	switch outputFormat {
	case "text":
		fmt.Print(solution.String())
	case "json":
		json, err := json.Marshal(solution)
		if err != nil {
			fatal("Couldn't encode the solution: %s", err.Error())
		}
		fmt.Print(string(json))
	case "ext-json":
		extJsonMap := make(map[string]interface{})
		extJsonMap["numNodes"] = params.NumNodes
		extJsonMap["numSlaves"] = params.NumSlaves
		extJsonMap["numVBuckets"] = params.NumVBuckets
		extJsonMap["numReplicas"] = params.NumReplicas
		extJsonMap["map"] = solution

		tags := make([]Tag, params.NumNodes)
		extJsonMap["tags"] = tags

		for i, t := range params.Tags {
			tags[i] = t
		}

		json, err := json.Marshal(extJsonMap)
		if err != nil {
			fatal("Couldn't encode the solution: %s", err.Error())
		}
		fmt.Print(string(json))
	default:
		panic("should not happen")
	}
}

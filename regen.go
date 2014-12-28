/*
Copyright 2014 Zachary Klippenstein

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
A library for generating random strings from regular expressions.
The generated strings will match the expressions they were generated from.

E.g.
	regen.Generate("[a-z0-9]{1,64}")
will return a lowercase alphanumeric string
between 1 and 64 characters long.

Expressions are parsed using the Go standard library's parser: http://golang.org/pkg/regexp/syntax/.

Constraints

"." will generate any character, not necessarily a printable one.

"x{0,}", "x*", and "x+" will generate a random number of x's up to an arbitrary limit.
If you care about the maximum number, specify it explicitly in the expression,
e.g. "x{0,256}".

Flags

Flags can be passed to the parser by setting them in the GeneratorArgs struct.
Newline flags are respected, and newlines won't be generated unless the appropriate flags for
matching them are set.

E.g.
Generate(".|[^a]") will never generate newlines. To generate newlines, create a generator and pass
the flag syntax.MatchNL.

The Perl character class flag is supported, and required if the pattern contains them.

Unicode groups are not supported at this time. Support may be added in the future.

Multi-threading

A generator is usually actually tree of generators, corresponding closely to the AST of the expression.
By default, generators run their children serially. In most cases, this is probably fine. However,
it can be changed by passing a different GeneratorExecutor in GeneratorArgs. NewForkJoinExecutor(), for example, will cause each
sub-generator to run in its own goroutine. This may improve or degrade performance, depending on the regular
expression.

A large bottleneck with running generators concurrently is actually the random source. Sources returned from
rand.NewSource() are unsafe for concurrent use. The default source, used by the top-level functions in the rand
package, is just a normal source guarded with a sync.Mutex. Ideally each goroutine would have exactly one source,
but the Seed function (called by NewSource()) is actually far slower than using the lock, even under high contention
(see the benchmarks at https://gist.github.com/zach-klippenstein/93b0d9f0499b1597754f for more information).
These are the results of the benchmarks on my Macbook Pro:
	BenchmarkReadFromUnlocked-4	200000000	 6.88 ns/op
	BenchmarkReadFromLocked-4	 10000000	  137 ns/op
	BenchmarkCreateUnlocked-4	   200000	18539 ns/op
For ForkJoinExecutor, it's cheaper to just use a locked source.
*/
package regen

import (
	"math/rand"
	"regexp/syntax"
)

/*
maxUpperBound is the number of instances to generate for unbounded repeat expressions.
E.g. ".*" will generate no more than maxUpperBound characters.

This value could change at any time, and should not be relied upon. If you care about the
upper bound, use something like ".{1,256}" in your expression.
*/
const maxUpperBound = 4096

type GeneratorArgs struct {
	// Default is rand.NewSource(rand.Int63()).
	// Does not need to be safe for concurrent use.
	RngSource rand.Source

	// Default is 0 (syntax.POSIX).
	Flags syntax.Flags

	// Used by Generators that execute multiple sub-generators.
	// Default is NewSerialExecutor().
	Executor GeneratorExecutor
}

// Generator generates random strings.
type Generator interface {
	Generate() string
}

type externalGenerator struct {
	GenArgs   *GeneratorArgs
	Generator *internalGenerator
}

/*
Generate a random string that matches the regular expression pattern.
If args is nil, default values are used.

This function does not seed the default RNG, so you must call rand.Seed() if you want
non-deterministic strings.
*/
func Generate(pattern string) (string, error) {
	generator, err := NewGenerator(pattern, nil)
	if err != nil {
		return "", err
	}
	return generator.Generate(), nil
}

// NewGenerator creates a generator that returns random strings that match the regular expression in pattern.
// If args is nil, default values are used.
func NewGenerator(pattern string, args *GeneratorArgs) (generator Generator, err error) {
	if nil == args {
		args = &GeneratorArgs{}
	}

	if nil == args.RngSource {
		args.RngSource = rand.NewSource(rand.Int63())
	}
	if nil == args.Executor {
		args.Executor = NewSerialExecutor()
	}

	// unicode groups only allowed with Perl
	if (args.Flags&syntax.UnicodeGroups) == syntax.UnicodeGroups && (args.Flags&syntax.Perl) != syntax.Perl {
		return nil, generatorError(nil, "UnicodeGroups not supported")
	}

	var regexp *syntax.Regexp
	regexp, err = syntax.Parse(pattern, args.Flags)
	if err != nil {
		return
	}

	var gen *internalGenerator
	gen, err = newGenerator(regexp, args)
	if err != nil {
		return
	}

	return &externalGenerator{
		GenArgs:   args,
		Generator: gen,
	}, nil
}

func (gen *externalGenerator) Generate() string {
	// Let the executor wrap the source if it needs to.
	originalSrc := gen.GenArgs.RngSource
	newSrc := gen.GenArgs.Executor.PrepareSource(originalSrc)

	runArgs := &runtimeArgs{
		Rng: rand.New(newSrc),
	}
	return gen.Generator.Generate(runArgs)
}

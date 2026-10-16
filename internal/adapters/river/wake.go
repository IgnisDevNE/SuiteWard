package river

// Wake is the set of job kinds a run asks the runtime to run soon
// (docs/contracts/runtime.md, "Queues and wake-ups").
type Wake []string

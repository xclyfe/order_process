package pipeline

import (
	"order_process/process/model/order"
)

// The interface of Pipleline Manager
type IPipelineManager interface {
	DispatchOrder(orderRecord *order.OrderRecord)
	Start()
}

// The definition of Order Process Pipeline Manager
type ProcessPipelineManager struct {
	pipelines                 []IPipeline
	lastPipelineSelectedIndex int
}

// The constructor of Order Process Pipeline Manager
func NewProcessPipelineManager(MaxPipelineCount int, NewPipeline func(func(string, IPipeline) ITaskHandler) IPipeline,
	NewTaskHandler func(string, IPipeline) ITaskHandler) *ProcessPipelineManager {
	pipelineManager := ProcessPipelineManager{
		lastPipelineSelectedIndex: -1,
	}
	for i := 0; i < MaxPipelineCount; i++ {
		pipelineManager.pipelines = append(pipelineManager.pipelines, NewPipeline(NewTaskHandler))
	}
	return &pipelineManager
}

// Start the pipeline management and pipelines
func (this *ProcessPipelineManager) Start() {
	for _, pipeline := range this.pipelines {
		pipeline.Start()
	}
}

// Dispatch order assigned to pipeline manager
func (this *ProcessPipelineManager) DispatchOrder(orderRecord *order.OrderRecord) {
	processJob := NewProcessJob(orderRecord)
	this.SelectPipeline().AppendJob(processJob)
}

// Round Robin Select pipeline
func (this *ProcessPipelineManager) SelectPipeline() IPipeline {
	if this.lastPipelineSelectedIndex+1 < len(this.pipelines) {
		this.lastPipelineSelectedIndex++
	} else {
		this.lastPipelineSelectedIndex = 0
	}
	return this.pipelines[this.lastPipelineSelectedIndex]
}

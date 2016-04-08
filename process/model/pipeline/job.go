package pipeline

import (
	"errors"
	"order_process/process/model/order"
	"time"
)

// The job interface
type IJob interface {
	// Job ID
	GetJobID() string

	// Service ID
	GetServiceID() string
	SetServiceID(string)

	// Step status
	GetCurrentStep() string
	IsCurrentStepCompleted() bool

	// Step
	StartStep(stepName string) error
	FinishCurrentStep() error

	// Job status
	IsJobInFinishingStep() bool
	IsJobFinished() bool

	// state in service
	GetJobStateInService(serviceID string) (string, error)

	// Failure
	IsErrorOccured() bool
	MarkJobAsFailure()

	// Rollback
	StartRollback()
	IsJobRollbacking() bool
	GetRollbackStep() (string, error)
	RollbackStep(stepName string) error

	// Save to database
	UpdateDatabase() error

	// Finalize
	FinalizeJob() error

	// Conversion
	ToMap() *map[string]interface{}
	ToJson() string
}

// The defination of Order Processing Job
type ProcessJob struct {
	record    *order.OrderRecord
	JobId     string
	ServiceId string
}

// The construtor of Order Processing Job
func NewProcessJob(rec *order.OrderRecord) *ProcessJob {
	return &ProcessJob{
		record:    rec,
		JobId:     rec.OrderID,
		ServiceId: rec.ServiceID,
	}
}

// Get the job id
func (this *ProcessJob) GetJobID() string {
	return this.JobId
}

// Get the service id
func (this *ProcessJob) GetServiceID() string {
	return this.ServiceId
}

// Get current order step
func (this *ProcessJob) GetCurrentStep() string {
	return this.record.CurrentStep
}

// Check whether current step is done.
func (this *ProcessJob) IsCurrentStepCompleted() bool {
	return this.record.Steps[len(this.record.Steps)-1].StepCompleted
}

// Check whether order is done
func (this *ProcessJob) IsJobFinished() bool {
	return this.record.Finished
}

// Check whether order is in final step("Completed" or "Failed")
func (this *ProcessJob) IsJobInFinishingStep() bool {
	return this.record.CurrentStep == Completed.String() || this.record.CurrentStep == Failed.String()
}

// To map format
func (this *ProcessJob) ToMap() *map[string]interface{} {
	return this.record.ToMap()
}

// To json format
func (this *ProcessJob) ToJson() string {
	str, err := this.record.ToJson()
	if err != nil {
		return ""
	}
	return str
}

// Start specified step
func (this *ProcessJob) StartStep(stepName string) error {
	if this.GetCurrentStep() == stepName {
		// The step has started
		return nil
	}

	if !this.IsCurrentStepCompleted() && !this.IsErrorOccured() {
		return errors.New("Last step not completed")
	}

	orderStep := order.OrderStep{
		StepName:  stepName,
		StartTime: time.Now().UTC().String(),
	}
	this.record.CurrentStep = orderStep.StepName
	this.record.Steps = append(this.record.Steps, orderStep)

	return this.UpdateDatabase()
}

//Finish the Current Step
func (this *ProcessJob) FinishCurrentStep() error {
	step := &this.record.Steps[len(this.record.Steps)-1]
	step.StepCompleted = true
	step.CompleteTime = time.Now().UTC().String()

	if this.IsJobInFinishingStep() && !this.IsJobRollbacking() {
		this.record.CompleteTime = step.CompleteTime
		this.record.Finished = true
	}
	return this.UpdateDatabase()
}

// Finalize job
func (this *ProcessJob) FinalizeJob() error {
	if this.IsJobInFinishingStep() && !this.IsJobRollbacking() {
		this.record.CompleteTime = time.Now().UTC().String()
		this.record.Finished = true

		return this.UpdateDatabase()
	}
	return errors.New("Job not ready to be finished")
}

// Check whether error occures during processing.
func (this *ProcessJob) IsErrorOccured() bool {
	return this.record.FailureOccured
}

// Mark the job as failure if error occurs
func (this *ProcessJob) MarkJobAsFailure() {
	this.record.FailureOccured = true
}

// Trigger the rollback process
func (this *ProcessJob) StartRollback() {
	this.record.RollbackState = order.Triggerred.String()
}

// Check whether job is rollbacking.
func (this *ProcessJob) IsJobRollbacking() bool {
	if this.record.RollbackState == order.Triggerred.String() {
		index := len(this.record.Steps) - 1
		for ; index >= 0; index-- {
			if this.record.Steps[index].StepName != Completed.String() &&
				this.record.Steps[index].StepName != Failed.String() &&
				!this.record.Steps[index].StepRollbacked {
				break
			}
		}
		return index >= 0
	}
	return false
}

// Get the step which needs rollback
func (this *ProcessJob) GetRollbackStep() (string, error) {
	index := len(this.record.Steps) - 1
	for ; index >= 0; index-- {
		if this.record.Steps[index].StepName != Completed.String() &&
			this.record.Steps[index].StepName != Failed.String() &&
			!this.record.Steps[index].StepRollbacked {
			break
		}
	}
	if index >= 0 {
		return this.record.Steps[index].StepName, nil
	} else {
		return "", errors.New("No more step need to be revoked.")
	}
}

// Perform the rollback of specified step
func (this *ProcessJob) RollbackStep(stepName string) error {
	index := len(this.record.Steps) - 1
	for ; index >= 0; index-- {
		if this.record.Steps[index].StepName == stepName &&
			!this.record.Steps[index].StepRollbacked {
			this.record.Steps[index].StepRollbacked = true
			break
		}
	}
	return this.UpdateDatabase()
}

// Update current job data to database
func (this *ProcessJob) UpdateDatabase() error {
	orderStateInService := order.OSS_Active.String()
	if this.IsJobFinished() && !this.IsJobRollbacking() {
		orderStateInService = order.OSS_Completed.String()
	}
	return this.record.SaveToDB(orderStateInService)
}

func (this *ProcessJob) GetJobStateInService(serviceID string) (string, error) {
	return this.record.GetOrderStateInService(serviceID)
}

func (this *ProcessJob) SetServiceID(serviceId string) {
	this.ServiceId = serviceId
	this.record.ServiceID = serviceId
}

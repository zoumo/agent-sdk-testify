package lifecycle

import (
	"errors"
)

func settleTransactionConnection(sessionDir, action, executionID, connection, status string) (ready, passed bool, err error) {
	passed = true
	err = updateIndex(sessionDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != executionID {
				continue
			}
			setConnectionStarted(execution, connection, status)
			ready = true
			for _, declared := range execution.Connections {
				if declared.Status == "pending" || declared.Status == "running" {
					ready = false
				}
				if declared.Status != "passed" {
					passed = false
				}
			}
			execution.Status = "open"
			execution.UpdatedAt = now()
			return nil
		}
		return errors.New(action + " execution absent from index")
	})
	return ready, passed, err
}

func failTransaction(session *Session, sessionDir, action, executionID, overlayRoot string, restore func() error) error {
	errs := []error{restore(), scrubExecutionAuthentication(session, overlayRoot)}
	errs = append(errs, updateIndex(sessionDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != executionID {
				continue
			}
			execution.Status = "failed"
			execution.ArtifactID = ""
			execution.UpdatedAt = now()
			for j := range execution.Connections {
				if execution.Connections[j].Status == "pending" || execution.Connections[j].Status == "running" {
					execution.Connections[j].Status = "failed"
				}
			}
			return nil
		}
		return errors.New(action + " execution absent from index")
	}))
	return errors.Join(errs...)
}

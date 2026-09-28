package config

import (
	"fmt"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// ResolveWorkloads builds ResolvedWorkloadSpec list from templates + workloads + legacy HTTP.
func (c *Config) ResolveWorkloads() ([]workload.ResolvedWorkloadSpec, error) {
	templates := make(map[string]workload.WorkloadTemplate, len(c.WorkloadTemplates))
	for name, tpl := range c.WorkloadTemplates {
		wt, err := convertTemplate(name, tpl)
		if err != nil {
			return nil, err
		}
		templates[name] = wt
	}

	specs := make([]workload.WorkloadSpec, 0, len(c.Workloads))
	for name, w := range c.Workloads {
		spec, err := convertWorkload(name, w)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}

	legacy := workload.LegacyHTTPInput{
		Name:             "http",
		PHPBinary:        c.PHP.Binary,
		WorkerScript:     c.PHP.WorkerScript,
		WorkingDirectory: c.PHP.WorkingDirectory,
		WorkerCount:      c.Workers.Count,
		MinReady:         c.Workers.MinReady,
		MemoryLimitMB:    c.Workers.MemoryLimitMB,
	}

	return workload.BuildFromConfig(templates, specs, legacy, c.HasExplicitWorkloads)
}

func convertTemplate(name string, tpl WorkloadTemplateConfig) (workload.WorkloadTemplate, error) {
	out := workload.WorkloadTemplate{
		Name:      name,
		Mode:      workload.WorkloadMode(tpl.Mode),
		Workers:   workload.WorkerPolicy{Min: 1, Max: 1},
		Resources: workload.DefaultResources(),
		Scaling:   workload.DefaultScaling(),
	}
	if tpl.Workers != nil {
		if tpl.Workers.Min != nil {
			out.Workers.Min = *tpl.Workers.Min
		}
		if tpl.Workers.Max != nil {
			out.Workers.Max = *tpl.Workers.Max
		}
	}
	if tpl.Resources != nil {
		if tpl.Resources.Class != nil {
			out.Resources.Class = workload.ResourceClass(*tpl.Resources.Class)
		}
		if tpl.Resources.MemoryMB != nil {
			out.Resources.MemoryMB = *tpl.Resources.MemoryMB
		}
	}
	if tpl.Scaling != nil {
		sc, err := convertScaling(*tpl.Scaling)
		if err != nil {
			return workload.WorkloadTemplate{}, fmt.Errorf("workload_templates.%s: %w", name, err)
		}
		out.Scaling = sc
	}
	return out, nil
}

func convertWorkload(name string, w WorkloadConfig) (workload.WorkloadSpec, error) {
	spec := workload.WorkloadSpec{
		Name:     name,
		Template: w.Template,
		Mode:     workload.WorkloadMode(w.Mode),
		Command:  append([]string(nil), w.Command...),
	}
	if w.Queue != nil {
		if w.Queue.Transport != nil {
			spec.Queue.Transport = *w.Queue.Transport
		}
		if w.Queue.Name != nil {
			spec.Queue.Name = *w.Queue.Name
		}
	}
	if w.Workers != nil {
		wp := &workload.WorkerPolicyPatch{}
		if w.Workers.Min != nil {
			wp.Min = w.Workers.Min
		}
		if w.Workers.Max != nil {
			wp.Max = w.Workers.Max
		}
		spec.Workers = wp
	}
	if w.Resources != nil {
		rp := &workload.ResourcePolicy{}
		if w.Resources.Class != nil {
			rp.Class = workload.ResourceClass(*w.Resources.Class)
		}
		if w.Resources.MemoryMB != nil {
			rp.MemoryMB = *w.Resources.MemoryMB
		}
		spec.Resources = rp
	}
	if w.Scaling != nil {
		sc, err := convertScaling(*w.Scaling)
		if err != nil {
			return workload.WorkloadSpec{}, fmt.Errorf("workloads.%s: %w", name, err)
		}
		spec.Scaling = &sc
	}
	return spec, nil
}

func convertScaling(s WorkloadScalingConfig) (workload.ScalingPolicy, error) {
	out := workload.DefaultScaling()
	if s.Strategy != nil {
		out.Strategy = workload.ScalingStrategyName(*s.Strategy)
	}
	var err error
	if s.ScaleUpCooldown != nil {
		out.ScaleUpCooldown, err = time.ParseDuration(*s.ScaleUpCooldown)
		if err != nil {
			return out, fmt.Errorf("invalid scale_up_cooldown: %w", err)
		}
	}
	if s.ScaleDownIdleFor != nil {
		out.ScaleDownIdleFor, err = time.ParseDuration(*s.ScaleDownIdleFor)
		if err != nil {
			return out, fmt.Errorf("invalid scale_down_idle_for: %w", err)
		}
	}
	if s.AvgJobDuration != nil {
		out.AvgJobDuration, err = time.ParseDuration(*s.AvgJobDuration)
		if err != nil {
			return out, fmt.Errorf("invalid avg_job_duration: %w", err)
		}
	}
	if s.TargetDrainTime != nil {
		out.TargetDrainTime, err = time.ParseDuration(*s.TargetDrainTime)
		if err != nil {
			return out, fmt.Errorf("invalid target_drain_time: %w", err)
		}
	}
	return out, nil
}

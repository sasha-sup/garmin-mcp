package garmin

// Decoding types for the subset of Garmin responses the server uses.
// Pointers mark values Garmin may omit or send as null. Field shapes were
// checked against live responses (November 2026), not only the OpenAPI spec.

// DailySummary is one item of /usersummary-service/v2/dailysummaries.
type DailySummary struct {
	CalendarDate string `json:"calendarDate"`
	Movement     struct {
		Steps struct {
			Value            *int     `json:"value"`
			Goal             *int     `json:"goal"`
			DistanceInMeters *float64 `json:"distanceInMeters"`
		} `json:"steps"`
		FloorsAscended struct {
			Value *float64 `json:"value"`
		} `json:"floorsAscended"`
	} `json:"movement"`
	Calories struct {
		BurnedResting *float64 `json:"burnedResting"`
		BurnedActive  *float64 `json:"burnedActive"`
		BurnedTotal   *float64 `json:"burnedTotal"`
	} `json:"calories"`
	HeartRate struct {
		MinValue     *int `json:"minValue"`
		MaxValue     *int `json:"maxValue"`
		RestingValue *int `json:"restingValue"`
	} `json:"heartRate"`
	IntensityMinutes struct {
		Goal     *int `json:"goal"`
		Moderate *int `json:"moderate"`
		Vigorous *int `json:"vigorous"`
	} `json:"intensityMinutes"`
	Stress struct {
		AvgLevel                   *int     `json:"avgLevel"`
		MaxLevel                   *int     `json:"maxLevel"`
		Qualifier                  string   `json:"qualifier"`
		RestDurationInMillis       *int64   `json:"restDurationInMillis"`
		LowStressDurationInMillis  *int64   `json:"lowStressDurationInMillis"`
		MedStressDurationInMillis  *int64   `json:"mediumStressDurationInMillis"`
		HighStressDurationInMillis *int64   `json:"highStressDurationInMillis"`
		HighStressProportion       *float64 `json:"highStressProportion"`
	} `json:"stress"`
	BodyBattery struct {
		MinValue       *int `json:"minValue"`
		MaxValue       *int `json:"maxValue"`
		ChargedValue   *int `json:"chargedValue"`
		DrainedValue   *int `json:"drainedValue"`
		LatestValue    *int `json:"latestValue"`
		ActivityEvents []struct {
			EventType           string `json:"eventType"`
			EventStartTimeLocal string `json:"eventStartTimeLocal"`
			BodyBatteryImpact   *int   `json:"bodyBatteryImpact"`
			DurationInMillis    *int64 `json:"durationInMillis"`
		} `json:"activityEvents"`
	} `json:"bodyBattery"`
	Respiration struct {
		AvgValue *float64 `json:"avgValue"`
		MinValue *float64 `json:"minValue"`
		MaxValue *float64 `json:"maxValue"`
	} `json:"respiration"`
	PulseOx struct {
		AvgValue *float64 `json:"avgValue"`
		MinValue *float64 `json:"minValue"`
	} `json:"pulseOx"`
}

// SleepScore is one entry of DailySleep.SleepScores.
type SleepScore struct {
	Value        *float64 `json:"value"`
	QualifierKey string   `json:"qualifierKey"`
}

// DailySleep is one item of /sleep-service/sleep/dailySleepSummariesByDate.
type DailySleep struct {
	CalendarDate             string                `json:"calendarDate"`
	SleepTimeSeconds         *int                  `json:"sleepTimeSeconds"`
	NapTimeSeconds           *int                  `json:"napTimeSeconds"`
	DeepSleepSeconds         *int                  `json:"deepSleepSeconds"`
	LightSleepSeconds        *int                  `json:"lightSleepSeconds"`
	RemSleepSeconds          *int                  `json:"remSleepSeconds"`
	AwakeSleepSeconds        *int                  `json:"awakeSleepSeconds"`
	SleepStartTimestampLocal *int64                `json:"sleepStartTimestampLocal"` // epoch ms shifted to local time
	SleepEndTimestampLocal   *int64                `json:"sleepEndTimestampLocal"`
	AwakeCount               *int                  `json:"awakeCount"`
	AvgSleepStress           *float64              `json:"avgSleepStress"`
	AvgHeartRate             *float64              `json:"avgHeartRate"`
	AverageSpO2Value         *float64              `json:"averageSpO2Value"`
	LowestSpO2Value          *float64              `json:"lowestSpO2Value"`
	AverageRespirationValue  *float64              `json:"averageRespirationValue"`
	SleepScoreFeedback       string                `json:"sleepScoreFeedback"`
	SleepScoreInsight        string                `json:"sleepScoreInsight"`
	SleepScores              map[string]SleepScore `json:"sleepScores"`
	BreathingDisruption      string                `json:"breathingDisruptionSeverity"`
	SleepNeed                *struct {
		Baseline *int   `json:"baseline"` // minutes
		Actual   *int   `json:"actual"`   // minutes
		Feedback string `json:"feedback"`
	} `json:"sleepNeed"`
}

// HRVSummary is one item of /hrv-service/hrv/daily/{from}/{to}.
type HRVSummary struct {
	CalendarDate      string `json:"calendarDate"`
	WeeklyAvg         *int   `json:"weeklyAvg"`
	LastNightAvg      *int   `json:"lastNightAvg"`
	LastNight5MinHigh *int   `json:"lastNight5MinHigh"`
	Baseline          *struct {
		LowUpper      *int `json:"lowUpper"`
		BalancedLow   *int `json:"balancedLow"`
		BalancedUpper *int `json:"balancedUpper"`
	} `json:"baseline"`
	Status         string `json:"status"`
	FeedbackPhrase string `json:"feedbackPhrase"`
}

// BodyBatteryFeedback is Garmin's textual Body Battery assessment.
type BodyBatteryFeedback struct {
	EventTimestampGmt string `json:"eventTimestampGmt"`
	BodyBatteryLevel  string `json:"bodyBatteryLevel"`
	FeedbackLongType  string `json:"feedbackLongType"`
}

// BodyBatteryReport is one item of /wellness-service/wellness/bodyBattery/reports/daily.
type BodyBatteryReport struct {
	Date        string       `json:"date"`
	Charged     *int         `json:"charged"`
	Drained     *int         `json:"drained"`
	Values      [][]*float64 `json:"bodyBatteryValuesArray"` // [epoch ms GMT, level]
	ActivityEvt []struct {
		EventType         string `json:"eventType"`
		EventStartTimeGmt string `json:"eventStartTimeGmt"`
		TimezoneOffset    *int64 `json:"timezoneOffset"` // ms
		DurationInMillis  *int64 `json:"durationInMilliseconds"`
		BodyBatteryImpact *int   `json:"bodyBatteryImpact"`
	} `json:"bodyBatteryActivityEvent"`
	Feedback      *BodyBatteryFeedback `json:"bodyBatteryDynamicFeedbackEvent"`
	EndOfDayFeedb *BodyBatteryFeedback `json:"endOfDayBodyBatteryDynamicFeedbackEvent"`
}

// DailyHeartRate is /wellness-service/wellness/dailyHeartRate.
type DailyHeartRate struct {
	CalendarDate        string       `json:"calendarDate"`
	MaxHeartRate        *int         `json:"maxHeartRate"`
	MinHeartRate        *int         `json:"minHeartRate"`
	RestingHeartRate    *int         `json:"restingHeartRate"`
	LastSevenDaysAvgRHR *int         `json:"lastSevenDaysAvgRestingHeartRate"`
	HeartRateValues     [][]*float64 `json:"heartRateValues"` // [epoch ms GMT, bpm]
	StartTimestampLocal string       `json:"startTimestampLocal"`
	StartTimestampGMT   string       `json:"startTimestampGMT"`
}

// TypeRef is a Garmin enum reference such as {"typeKey":"running"}.
type TypeRef struct {
	TypeKey string `json:"typeKey"`
}

// Activity is one item of /activitylist-service/activities/search/activities.
type Activity struct {
	ActivityID            int64    `json:"activityId"`
	ActivityName          string   `json:"activityName"`
	StartTimeLocal        string   `json:"startTimeLocal"`
	ActivityType          TypeRef  `json:"activityType"`
	EventType             TypeRef  `json:"eventType"`
	Distance              *float64 `json:"distance"`        // m
	Duration              *float64 `json:"duration"`        // s
	MovingDuration        *float64 `json:"movingDuration"`  // s
	ElapsedDuration       *float64 `json:"elapsedDuration"` // s
	AverageSpeed          *float64 `json:"averageSpeed"`    // m/s
	MaxSpeed              *float64 `json:"maxSpeed"`
	ElevationGain         *float64 `json:"elevationGain"`
	Calories              *float64 `json:"calories"`
	AverageHR             *float64 `json:"averageHR"`
	MaxHR                 *float64 `json:"maxHR"`
	Steps                 *int     `json:"steps"`
	AvgPower              *float64 `json:"avgPower"`
	AvgRunCadence         *float64 `json:"averageRunningCadenceInStepsPerMinute"`
	AerobicTrainingEffect *float64 `json:"aerobicTrainingEffect"`
	AnaerobicTrainingEff  *float64 `json:"anaerobicTrainingEffect"`
	TrainingEffectLabel   string   `json:"trainingEffectLabel"`
	ActivityTrainingLoad  *float64 `json:"activityTrainingLoad"`
	VO2MaxValue           *float64 `json:"vO2MaxValue"`
	DifferenceBodyBattery *int     `json:"differenceBodyBattery"`
	ModerateIntensityMins *int     `json:"moderateIntensityMinutes"`
	VigorousIntensityMins *int     `json:"vigorousIntensityMinutes"`
	TotalSets             *int     `json:"totalSets"`
	TotalReps             *int     `json:"totalReps"`
	LapCount              *int     `json:"lapCount"`
	HRTimeInZone1         *float64 `json:"hrTimeInZone_1"`
	HRTimeInZone2         *float64 `json:"hrTimeInZone_2"`
	HRTimeInZone3         *float64 `json:"hrTimeInZone_3"`
	HRTimeInZone4         *float64 `json:"hrTimeInZone_4"`
	HRTimeInZone5         *float64 `json:"hrTimeInZone_5"`
	IsPR                  bool     `json:"pr"`
	ManualActivity        bool     `json:"manualActivity"`
}

// ActivityDetail is /activity-service/activity/{id}.
type ActivityDetail struct {
	ActivityID   int64          `json:"activityId"`
	ActivityName string         `json:"activityName"`
	ActivityType TypeRef        `json:"activityTypeDTO"`
	EventType    TypeRef        `json:"eventTypeDTO"`
	Summary      map[string]any `json:"summaryDTO"`
}

// WeightRange is /weight-service/weight/range/{start}/{end}.
type WeightRange struct {
	Daily []struct {
		SummaryDate string        `json:"summaryDate"`
		Latest      *WeightSample `json:"latestWeight"`
	} `json:"dailyWeightSummaries"`
}

// WeightSample is one weigh-in. Masses are in grams.
type WeightSample struct {
	Weight     *float64 `json:"weight"`
	BMI        *float64 `json:"bmi"`
	BodyFat    *float64 `json:"bodyFat"` // %
	BodyWater  *float64 `json:"bodyWater"`
	MuscleMass *float64 `json:"muscleMass"`
	BoneMass   *float64 `json:"boneMass"`
	SourceType string   `json:"sourceType"`
}

// HeartRateZones is one item of /biometric-service/heartRateZones/v2.
type HeartRateZones struct {
	Sport          string `json:"sport"`
	TrainingMethod string `json:"trainingMethod"`
	RestingHR      *int   `json:"restingHeartRateUsed"`
	LactateHR      *int   `json:"lactateThresholdHeartRateUsed"`
	MaxHR          *int   `json:"maxHeartRateUsed"`
	Zone1Floor     *int   `json:"zone1Floor"`
	Zone2Floor     *int   `json:"zone2Floor"`
	Zone3Floor     *int   `json:"zone3Floor"`
	Zone4Floor     *int   `json:"zone4Floor"`
	Zone5Floor     *int   `json:"zone5Floor"`
}

// FitnessAge is /fitnessage-service/fitnessage/v2/{date}.
type FitnessAge struct {
	ChronologicalAge     *int     `json:"chronologicalAge"`
	FitnessAge           *float64 `json:"fitnessAge"`
	AchievableFitnessAge *float64 `json:"achievableFitnessAge"`
	PreviousFitnessAge   *float64 `json:"previousFitnessAge"`
	LastUpdated          string   `json:"lastUpdated"`
	Components           map[string]struct {
		Value       *float64 `json:"value"`
		TargetValue *float64 `json:"targetValue"`
		Stale       bool     `json:"stale"`
	} `json:"components,omitempty"`
}

// ProfileSettings is /userprofile-service/userprofile/settings (subset).
type ProfileSettings struct {
	TimeZone          string `json:"timeZone"`
	MeasurementSystem string `json:"measurementSystem"`
	PreferredLocale   string `json:"preferredLocale"`
}

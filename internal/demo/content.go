package demo

import (
	"fmt"
	"math/rand"
	"strings"
)

// The demo org, its people, and its clients. Everything here is invented;
// the domains are reserved example domains so no mail can reach anyone.

type person struct {
	First, Last string
}

var firstNames = []string{
	"Ada", "Grace", "Linus", "Margaret", "Dennis", "Barbara", "Ken", "Radia", "Guido", "Hedy",
	"Alan", "Katherine", "Tim", "Frances", "Bjarne", "Anita", "James", "Shafi", "Yukihiro", "Mary",
	"Edsger", "Jean", "Donald", "Adele", "Brendan", "Sophie", "Rasmus", "Karen", "Rob", "Leslie",
	"Ken", "Joan", "Anders", "Lynn", "Larry", "Evelyn", "Rich", "Priya", "Tomas", "Amara",
	"Mateo", "Ngozi", "Wei", "Ines", "Kwame", "Sana", "Luca", "Aoife", "Jonas", "Farah",
}

var lastNames = []string{
	"Okafor", "Lindqvist", "Marchetti", "Haddad", "Nakamura", "Ferreira", "Novak", "Osei", "Brennan", "Kowalski",
	"Delgado", "Iyer", "Sørensen", "Abubakar", "Petrov", "Nkemelu", "Fischer", "Moreau", "Tanaka", "Quintero",
	"Adeyemi", "Castellano", "Hoffmann", "Rahman", "Silva", "Varga", "O'Neill", "Mensah", "Larsen", "Dubois",
	"Yilmaz", "Kim", "Rossi", "Bakker", "Haruna", "Andersen", "Costa", "Zhang", "Murphy", "Schneider",
	"Nair", "Eriksen", "Diallo", "Roux", "Ivanova", "Santos", "Byrne", "Weber", "Mbeki", "Almeida",
}

// skill pools, by the kind of role a job is
var skillPools = map[string][]string{
	"backend":  {"go", "postgres", "kubernetes", "grpc", "redis", "kafka", "terraform", "aws"},
	"frontend": {"typescript", "react", "css", "vite", "playwright", "graphql", "accessibility", "node"},
	"data":     {"python", "spark", "airflow", "dbt", "postgres", "kafka", "snowflake", "sql"},
	"ml":       {"python", "pytorch", "mlflow", "feature stores", "sql", "kubernetes", "docker", "ray"},
	"mobile":   {"kotlin", "swift", "jetpack compose", "swiftui", "graphql", "ci/cd", "firebase", "rest"},
	"sre":      {"kubernetes", "terraform", "prometheus", "go", "linux", "aws", "incident response", "bash"},
	"payments": {"java", "kotlin", "postgres", "kafka", "pci-dss", "idempotency", "grpc", "aws"},
}

var employers = []string{
	"Fjord Systems", "Bluewater Labs", "Kestrel Analytics", "Harbourline", "Northgate Software", "Tessellate",
	"Orbital Freight", "Copperleaf", "Meridian Health Tech", "Quillstack", "Saltmarsh", "Ridgeway Robotics",
}

var cities = []string{"Berlin", "London", "Amsterdam", "Lisbon", "Warsaw", "Dublin", "Stockholm", "Barcelona", "New York", "Toronto"}

var seniorities = []string{"junior", "mid", "senior", "staff"}

// client is one company the agency recruits for.
type client struct {
	Name, Domain, City, Contact string
	Jobs                        []jobSpec
}

// jobSpec is one role: which library process it runs, and the copy the
// recruiter posted.
type jobSpec struct {
	Title, Process, Family, Seniority, Remote string
	SalaryMin, SalaryMax                      int
	Blind                                     bool
	// Automate marks the job whose assessment stage decides on its score.
	Automate bool
	Skills   []string
	Summary  string
}

var clients = []client{
	{
		Name: "Globex Logistics", Domain: "globex-logistics.example", City: "Berlin", Contact: "Hannah Weiss",
		Jobs: []jobSpec{
			{Title: "Backend Engineer (Go)", Process: "engineering_loop", Family: "backend", Seniority: "senior", Remote: "hybrid",
				SalaryMin: 85000, SalaryMax: 105000, Automate: true,
				Skills:  []string{"go", "postgres", "kubernetes", "grpc"},
				Summary: "Own the routing services that plan a million parcel journeys a night. You will design APIs other teams build on, take the pager for what you ship, and mentor two engineers."},
			{Title: "Data Platform Engineer", Process: "agency_standard", Family: "data", Seniority: "mid", Remote: "hybrid",
				SalaryMin: 70000, SalaryMax: 90000,
				Skills:  []string{"python", "spark", "airflow", "postgres"},
				Summary: "Build the pipelines that turn depot scans into the numbers the operations floor runs on. Airflow, Spark, and a warehouse you will help choose."},
		},
	},
	{
		Name: "Initech Payments", Domain: "initech-payments.example", City: "London", Contact: "Peter Gibbons",
		Jobs: []jobSpec{
			{Title: "Senior Payments Engineer", Process: "engineering_loop", Family: "payments", Seniority: "senior", Remote: "remote", Blind: true,
				SalaryMin: 95000, SalaryMax: 125000,
				Skills:  []string{"java", "kotlin", "postgres", "kafka", "idempotency"},
				Summary: "Card authorisations, settlement files, and the reconciliation that catches every penny. Correctness under retries is the whole job."},
			{Title: "Frontend Engineer (TypeScript)", Process: "fast_track", Family: "frontend", Seniority: "mid", Remote: "remote",
				SalaryMin: 65000, SalaryMax: 85000,
				Skills:  []string{"typescript", "react", "graphql", "playwright"},
				Summary: "The merchant dashboard: charts that load fast, forms that never lose a keystroke, and an accessibility bar we do not lower."},
		},
	},
	{
		Name: "Umbrella Health", Domain: "umbrella-health.example", City: "Amsterdam", Contact: "Dr. Marta Visser",
		Jobs: []jobSpec{
			{Title: "Full-Stack Developer", Process: "agency_standard", Family: "backend", Seniority: "mid", Remote: "onsite",
				SalaryMin: 60000, SalaryMax: 78000,
				Skills:  []string{"go", "typescript", "postgres", "react"},
				Summary: "Clinician-facing tools where a confusing screen costs minutes that matter. Small team, whole features, real users down the corridor."},
			{Title: "Site Reliability Engineer", Process: "fast_track", Family: "sre", Seniority: "senior", Remote: "hybrid",
				SalaryMin: 80000, SalaryMax: 100000,
				Skills:  []string{"kubernetes", "terraform", "prometheus", "go"},
				Summary: "Keep patient records available through upgrades, incidents, and audits. You will write the runbooks and then automate them away."},
		},
	},
	{
		Name: "Vandelay Industries", Domain: "vandelay.example", City: "New York", Contact: "Art Vandelay",
		Jobs: []jobSpec{
			{Title: "Machine Learning Engineer", Process: "agency_standard", Family: "ml", Seniority: "senior", Remote: "remote",
				SalaryMin: 140000, SalaryMax: 175000,
				Skills:  []string{"python", "pytorch", "mlflow", "sql"},
				Summary: "Demand forecasting for an import/export book that moves with the weather. Models that ship, are monitored, and get retrained without a meeting."},
			{Title: "Mobile Engineer (Kotlin/Swift)", Process: "engineering_loop", Family: "mobile", Seniority: "mid", Remote: "remote",
				SalaryMin: 110000, SalaryMax: 135000,
				Skills:  []string{"kotlin", "swift", "jetpack compose", "swiftui"},
				Summary: "One app on two platforms for the field agents who photograph, sign, and file from the warehouse floor. Offline first, always."},
		},
	},
}

// jobDescription is the posting as the recruiter wrote it: what the role is,
// what they want, what is on offer.
func jobDescription(c client, j jobSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## About the role\n\n%s\n\n", j.Summary)
	fmt.Fprintf(&b, "## What we are looking for\n\n")
	for _, s := range j.Skills {
		fmt.Fprintf(&b, "- Real production experience with %s\n", s)
	}
	fmt.Fprintf(&b, "- %s-level ownership: you scope work, ship it, and stand behind it\n", strings.Title(j.Seniority))
	fmt.Fprintf(&b, "- Clear writing; most of our design happens in documents\n\n")
	fmt.Fprintf(&b, "## Details\n\n- %s, %s\n- %s\n- €%s to €%s, depending on level\n", c.City, j.Remote, c.Name, thousands(j.SalaryMin), thousands(j.SalaryMax))
	return b.String()
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	return s[:len(s)-3] + "," + s[len(s)-3:]
}

// candidate is one generated person: their contact details, the skills
// their résumé claims, and the résumé itself.
type candidate struct {
	Name, Email, Phone, City, Headline, Seniority string
	Links                                         []string
	Skills                                        []string
	Family                                        string
	Resume                                        []string // paragraphs
}

// people generates n candidates with a deterministic random source, spread
// across the job families so every role has applicants and the talent
// network has a spread of skills.
func people(r *rand.Rand, n int, slug string) []candidate {
	families := make([]string, 0, len(skillPools))
	for f := range skillPools {
		families = append(families, f)
	}
	sortStrings(families)
	out := make([]candidate, 0, n)
	used := map[string]bool{}
	for len(out) < n {
		first, last := firstNames[r.Intn(len(firstNames))], lastNames[r.Intn(len(lastNames))]
		key := first + " " + last
		if used[key] {
			continue
		}
		used[key] = true
		family := families[len(out)%len(families)]
		pool := skillPools[family]
		perm := r.Perm(len(pool))
		skills := make([]string, 0, 5)
		for _, i := range perm[:4+r.Intn(2)] {
			skills = append(skills, pool[i])
		}
		sortStrings(skills)
		seniority := seniorities[weighted(r, []int{1, 4, 4, 1})]
		city := cities[r.Intn(len(cities))]
		handle := strings.ToLower(strings.NewReplacer("'", "", "ø", "o", "ö", "o").Replace(first + "." + last))
		c := candidate{
			Name: key, Email: handle + "@" + slug + "-candidates.example", Phone: fmt.Sprintf("+44 7%03d %06d", r.Intn(1000), r.Intn(1000000)),
			City: city, Seniority: seniority, Family: family, Skills: skills,
			Links: []string{"https://github.com/" + strings.ReplaceAll(handle, ".", "-")},
		}
		c.Headline = headline(seniority, family)
		c.Resume = resumeParagraphs(r, c)
		out = append(out, c)
	}
	return out
}

func headline(seniority, family string) string {
	role := map[string]string{
		"backend": "Backend Engineer", "frontend": "Frontend Engineer", "data": "Data Engineer", "ml": "Machine Learning Engineer",
		"mobile": "Mobile Engineer", "sre": "Site Reliability Engineer", "payments": "Payments Engineer",
	}[family]
	switch seniority {
	case "junior":
		return "Junior " + role
	case "senior":
		return "Senior " + role
	case "staff":
		return "Staff " + role
	}
	return role
}

// resumeParagraphs writes a plausible one-page résumé.
func resumeParagraphs(r *rand.Rand, c candidate) []string {
	years := map[string]int{"junior": 1 + r.Intn(2), "mid": 3 + r.Intn(3), "senior": 6 + r.Intn(5), "staff": 10 + r.Intn(6)}[c.Seniority]
	out := []string{
		c.Name,
		c.Headline + " · " + c.City + " · " + c.Email + " · " + c.Phone,
		"",
		"Summary",
		fmt.Sprintf("%s with %d years building and running production systems, most recently with %s. Comfortable owning a service end to end: design, delivery, on-call, and the write-up afterwards.",
			c.Headline, years, joinAnd(c.Skills[:2])),
		"",
		"Skills",
		strings.Join(c.Skills, ", "),
		"",
		"Experience",
	}
	left := years
	perm := r.Perm(len(employers))
	for i := 0; left > 0 && i < 3; i++ {
		span := 1 + r.Intn(3)
		if span > left {
			span = left
		}
		to := 2026 - (years - left)
		from := to - span
		left -= span
		out = append(out,
			fmt.Sprintf("%s — %s, %d to %d", employers[perm[i]], c.Headline, from, to),
			fmt.Sprintf("Shipped %s on a team of %d. %s.", achievement(r, c.Family), 3+r.Intn(6), impact(r)),
			"")
	}
	out = append(out, "Education", fmt.Sprintf("BSc Computer Science, University of %s, %d", cities[r.Intn(len(cities))], 2026-years-4))
	return out
}

func achievement(r *rand.Rand, family string) string {
	options := map[string][]string{
		"backend":  {"a rate-limited public API serving 40k requests a second", "an event-sourced order service on Kafka", "a zero-downtime Postgres migration of 2TB"},
		"frontend": {"a design system used by nine product teams", "a checkout flow that lifted conversion by 6%", "an accessible data grid with virtual scrolling"},
		"data":     {"a nightly pipeline over 300M events with Airflow and Spark", "a dbt model layer replacing 200 hand-written reports", "a late-arriving-data reconciliation job"},
		"ml":       {"a demand forecaster in production with drift monitoring", "a feature store shared by four models", "a ranking model that lifted click-through 11%"},
		"mobile":   {"an offline-first sync engine on Android and iOS", "a camera capture flow with on-device OCR", "a shared Kotlin Multiplatform core"},
		"sre":      {"a Kubernetes platform hosting 120 services", "an incident process that cut MTTR by half", "Terraform modules adopted org-wide"},
		"payments": {"an idempotent authorisation gateway", "settlement file generation for three acquirers", "a reconciliation engine matching 99.98% automatically"},
	}[family]
	return options[r.Intn(len(options))]
}

func impact(r *rand.Rand) string {
	return []string{
		"Wrote the design documents and ran the reviews",
		"Introduced structured on-call and the postmortem template the org still uses",
		"Mentored two junior engineers through their first production launches",
		"Cut the build from 25 minutes to 6",
		"Owned the vendor relationship and the quarterly cost review",
	}[r.Intn(5)]
}

func joinAnd(parts []string) string {
	if len(parts) == 2 {
		return parts[0] + " and " + parts[1]
	}
	return strings.Join(parts, ", ")
}

func weighted(r *rand.Rand, weights []int) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	n := r.Intn(total)
	for i, w := range weights {
		if n < w {
			return i
		}
		n -= w
	}
	return len(weights) - 1
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// clientSpec finds the invented client a stored company row came from.
func clientSpec(c clientRow) client {
	for _, cl := range clients {
		if cl.Name == c.Name {
			return cl
		}
	}
	return client{}
}

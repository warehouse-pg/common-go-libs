package dbconn_test

import (
	"strings"

	"github.com/blang/semver"
	"github.com/greenplum-db/gp-common-go-libs/dbconn"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("dbconn/version tests", func() {
	fake43 := dbconn.GPDBVersion{VersionString: "4.3.0.0", SemVer: semver.MustParse("4.3.0")}
	fake50 := dbconn.GPDBVersion{VersionString: "5.0.0", SemVer: semver.MustParse("5.0.0")}
	fake51 := dbconn.GPDBVersion{VersionString: "5.1.0", SemVer: semver.MustParse("5.1.0")}
	Describe("StringToSemVerRange", func() {
		v400 := semver.MustParse("4.0.0")
		v500 := semver.MustParse("5.0.0")
		v510 := semver.MustParse("5.1.0")
		v501 := semver.MustParse("5.0.1")
		It(`turns "=5" into a range matching 5.x`, func() {
			resultRange := dbconn.StringToSemVerRange("=5")
			Expect(resultRange(v400)).To(BeFalse())
			Expect(resultRange(v500)).To(BeTrue())
			Expect(resultRange(v510)).To(BeTrue())
			Expect(resultRange(v501)).To(BeTrue())
		})
		It(`turns "=5.0" into a range matching 5.0.x`, func() {
			resultRange := dbconn.StringToSemVerRange("=5.0")
			Expect(resultRange(v400)).To(BeFalse())
			Expect(resultRange(v500)).To(BeTrue())
			Expect(resultRange(v510)).To(BeFalse())
			Expect(resultRange(v501)).To(BeTrue())
		})
		It(`turns "=5.0.0" into a range matching 5.0.0`, func() {
			resultRange := dbconn.StringToSemVerRange("=5.0.0")
			Expect(resultRange(v400)).To(BeFalse())
			Expect(resultRange(v500)).To(BeTrue())
			Expect(resultRange(v510)).To(BeFalse())
			Expect(resultRange(v501)).To(BeFalse())
		})
	})
	Describe("Before", func() {
		It("returns true when comparing 4.3 to 5", func() {
			connection.Version = fake43
			result := connection.Version.Before("5")
			Expect(result).To(BeTrue())
		})
		It("returns true when comparing 5 to 5.1", func() {
			connection.Version = fake50
			result := connection.Version.Before("5.1")
			Expect(result).To(BeTrue())
		})
		It("returns false when comparing 5 to 5", func() {
			connection.Version = fake50
			result := connection.Version.Before("5")
			Expect(result).To(BeFalse())
		})
	})
	Describe("AtLeast", func() {
		It("returns true when comparing 5 to 4.3", func() {
			connection.Version = fake50
			result := connection.Version.AtLeast("4")
			Expect(result).To(BeTrue())
		})
		It("returns true when comparing 5 to 5", func() {
			connection.Version = fake50
			result := connection.Version.AtLeast("5")
			Expect(result).To(BeTrue())
		})
		It("returns true when comparing 5.1 to 5.0", func() {
			connection.Version = fake51
			result := connection.Version.AtLeast("5")
			Expect(result).To(BeTrue())
		})
		It("returns false when comparing 4.3 to 5", func() {
			connection.Version = fake43
			result := connection.Version.AtLeast("5")
			Expect(result).To(BeFalse())
		})
		It("returns false when comparing 5.0 to 5.1", func() {
			connection.Version = fake50
			result := connection.Version.AtLeast("5.1")
			Expect(result).To(BeFalse())
		})
	})
	Describe("Is", func() {
		It("returns true when comparing 5 to 5", func() {
			connection.Version = fake50
			result := connection.Version.Is("5")
			Expect(result).To(BeTrue())
		})
		It("returns true when comparing 5.1 to 5", func() {
			connection.Version = fake51
			result := connection.Version.Is("5")
			Expect(result).To(BeTrue())
		})
		It("returns false when comparing 5.0 to 5.1", func() {
			connection.Version = fake50
			result := connection.Version.Is("5.1")
			Expect(result).To(BeFalse())
		})
		It("returns false when comparing 4.3 to 5", func() {
			connection.Version = fake43
			result := connection.Version.Is("5")
			Expect(result).To(BeFalse())
		})
	})
	// These banner fixtures are not hand-guessed: they're reconstructed from the
	// actual template strings in the warehouse-pg/warehouse-pg (WHPG6/WHPG7) and
	// warehouse-pg-next (WHPG19) source trees.
	//   - WHPG6 (tag 6.27.5-WHPG) / WHPG7 (tag 7.5.0-WHPG): configure.in's
	//     AC_DEFINE_UNQUOTED(PG_VERSION_STR, ["PostgreSQL $PG_VERSION (Greenplum
	//     Database $GP_VERSION) on $host, compiled by $cc_string, N-bit"]), where
	//     GP_VERSION comes from ./getversion as "<git-describe> build <BUILDNUMBER>"
	//     (e.g. "6.27.5-WHPG build dev"); version.c then appends
	//     " compiled on <date> <time>", " Bhuvnesh C." (7.x only), " WarehousePG".
	//   - WHPG19: src/backend/utils/adt/version.c literally does
	//     strcat(version, " (Greenplum Database) " GP_VERSION) - confirmed against
	//     the live "PostgreSQL 19beta1 on aarch64-unknown-linux-gnu, compiled by gcc
	//     (GCC) 11.5.0 ... (Greenplum Database) 19.0.0 build dev ... WarehousePG"
	//     banner from a real running whpg19-live cluster.
	Describe("ParseGPVersion", func() {
		It("parses a real WHPG6 banner (reconstructed from warehouse-pg tag 6.27.5-WHPG)", func() {
			banner := "PostgreSQL 9.4.26 (Greenplum Database 6.27.5-WHPG build dev) on x86_64-pc-linux-gnu, compiled by gcc (GCC) 6.4.0, 64-bit compiled on Nov 15 2024 WarehousePG"
			versionAndTrailer, version, err := dbconn.ParseGPVersion(banner)
			Expect(err).ToNot(HaveOccurred())
			Expect(version).To(Equal(semver.MustParse("6.27.5")))
			Expect(versionAndTrailer).To(HavePrefix("6.27.5-WHPG"))
		})
		It("parses a real WHPG7 banner (reconstructed from warehouse-pg tag 7.5.0-WHPG)", func() {
			banner := "PostgreSQL 12.12 (Greenplum Database 7.5.0-WHPG build dev) on x86_64-pc-linux-gnu, compiled by gcc (GCC) 9.4.0, 64-bit compiled on Jan 1 2025 Bhuvnesh C. WarehousePG"
			versionAndTrailer, version, err := dbconn.ParseGPVersion(banner)
			Expect(err).ToNot(HaveOccurred())
			Expect(version).To(Equal(semver.MustParse("7.5.0")))
			Expect(versionAndTrailer).To(HavePrefix("7.5.0-WHPG"))
		})
		It("parses the real WHPG19 banner captured live from the whpg19-live cluster", func() {
			banner := "PostgreSQL 19beta1 on aarch64-unknown-linux-gnu, compiled by gcc (GCC) 11.5.0 20240719 (Red Hat 11.5.0-14), 64-bit (Greenplum Database) 19.0.0 build dev compiled on Jul 20 2026 08:21:10 Bhuvnesh C. WarehousePG"
			versionAndTrailer, version, err := dbconn.ParseGPVersion(banner)
			Expect(err).ToNot(HaveOccurred())
			Expect(version).To(Equal(semver.MustParse("19.0.0")))
			Expect(versionAndTrailer).To(HavePrefix("19.0.0"))
		})
		It("does not mistake the gcc toolchain version for the GP version", func() {
			banner := "PostgreSQL 19beta1 on aarch64-unknown-linux-gnu, compiled by gcc (GCC) 11.5.0 20240719 (Red Hat 11.5.0-14), 64-bit (Greenplum Database) 19.0.0 build dev compiled on Jul 20 2026 08:21:10 Bhuvnesh C. WarehousePG"
			_, version, err := dbconn.ParseGPVersion(banner)
			Expect(err).ToNot(HaveOccurred())
			Expect(version).ToNot(Equal(semver.MustParse("11.5.0")))
		})
		It("keeps the leading gcc version out of the returned version-and-trailer string, so callers that re-derive the major version from it (e.g. strings.Split(v, \".\")[0]) don't silently pick up 11 instead of 19", func() {
			banner := "PostgreSQL 19beta1 on aarch64-unknown-linux-gnu, compiled by gcc (GCC) 11.5.0 20240719 (Red Hat 11.5.0-14), 64-bit (Greenplum Database) 19.0.0 build dev compiled on Jul 20 2026 08:21:10 Bhuvnesh C. WarehousePG"
			versionAndTrailer, _, err := dbconn.ParseGPVersion(banner)
			Expect(err).ToNot(HaveOccurred())
			Expect(versionAndTrailer).ToNot(ContainSubstring("11.5.0"))
			Expect(strings.Split(versionAndTrailer, ".")[0]).To(Equal("19"))
		})
		It("returns an error instead of panicking when the marker is missing", func() {
			_, _, err := dbconn.ParseGPVersion("this is not a GPDB version string at all")
			Expect(err).To(HaveOccurred())
		})
		It("returns an error instead of panicking when a marker is present but no version number follows", func() {
			_, _, err := dbconn.ParseGPVersion("(Greenplum Database) no version number here")
			Expect(err).To(HaveOccurred())
		})
	})
})

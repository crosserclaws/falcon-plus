package owl

import (
	"fmt"

	gormExt "github.com/Cepave/open-falcon-backend/common/gorm"
	owlModel "github.com/Cepave/open-falcon-backend/common/model/owl"
)

type ErrNotInSameHierarchy struct {
	message string
}

func (err ErrNotInSameHierarchy) Error() string {
	return err.message
}

type cityInfo struct {
	ProvinceId   int16  `db:"pv_id"`
	ProvinceName string `db:"pv_name"`
	CityId       int16  `db:"ct_id"`
	CityName     string `db:"ct_name"`
}

// Checks if the hierarchy for province and city are in the same administrative region
func CheckHierarchyForCity(provinceId int16, cityId int16) error {
	if cityId == -1 {
		return nil
	}

	cityInfo := &cityInfo{}
	DbFacade.SqlxDbCtrl.Get(
		cityInfo,
		`
		SELECT pv_id, pv_name, ct_id, ct_name
		FROM owl_province
			INNER JOIN
			owl_city
			ON pv_id = ct_pv_id
				AND ct_id = ?
		`,
		cityId,
	)

	if cityInfo.ProvinceId != provinceId {
		return ErrNotInSameHierarchy{
			message: fmt.Sprintf(
				"City[ID: %d][%s] should be belonging to province[ID: %d]. But got province[ID: %d][%s]",
				cityInfo.CityId, cityInfo.CityName,
				provinceId,
				cityInfo.ProvinceId, cityInfo.ProvinceName,
			),
		}
	}

	return nil
}

func GetProvincesByName(name string) []*owlModel.Province {
	var q = DbFacade.GormDb.Model(&owlModel.Province{}).
		Select(`
		*
	`).
		Where(`
		pv_name LIKE ?
		`,
		name+"%",
	)

	var results []*owlModel.Province
	gormExt.ToDefaultGormDbExt(q.Find(&results))

	return results
}

func GetCitiesByName(name string) []*owlModel.City {
	var q = DbFacade.GormDb.Model(&owlModel.City{}).
		Select(`
		*
	`).
		Where(`
		ct_name LIKE ?
		`,
		name+"%",
	)

	var results []*owlModel.City
	gormExt.ToDefaultGormDbExt(q.Find(&results))

	return results
}

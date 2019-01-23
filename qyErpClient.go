package main

/*
 * @Description: file content
 * @Author: 陈洋
 * @Date: 2018-11-29 10:42:18
 */

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cihub/seelog"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/mysql"
)

// ErrorNotFound 没有找到记录
var ErrorNotFound = "record not found"

// ErrorNotFoundAreas 没有找到商圈
var ErrorNotFoundAreas = "没有找到商圈数据库"
var cylog seelog.LoggerInterface

// SeelogConfigPath 日志文件设置存放路径
var SeelogConfigPath = "./config/cy_seelog.xml"

// AppConfigPath 日志文件设置存放路径
var AppConfigPath = "./config/app.json"

var app App

// App 配置信息
type App struct {
	Server struct {
		Port int `json:"port"`
	} `json:"server"`
	Qy struct {
		DB       *gorm.DB `json:"-"`
		Name     string   `json:"name"`
		IP       string   `json:"ip"`
		Port     int      `json:"port"`
		User     string   `json:"user"`
		Password string   `json:"password"`
		Dbname   string   `json:"dbname"`
	} `json:"qy"`
	Rs struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	} `json:"rs"`
}

func main() {
	var err error

	cylog, err = seelog.LoggerFromConfigAsFile(SeelogConfigPath)
	if err != nil {
		fmt.Println("读取 seelog.xml 日志配置文件错误,请确定格式:", err.Error())
		return
	}

	b, err := ioutil.ReadFile(AppConfigPath)
	if err != nil {
		cylog.Error("读取配置文件错误：", err.Error())
		return
	}
	err = json.Unmarshal(b, &app)
	if err != nil {
		cylog.Error("解析配置文件错误：", err.Error())
		return
	}
	cylog.Infof("群宴数据库配置信息%+v", app.Qy)
	cylog.Infof("服务器配置信息%+v", app.Server)
	cylog.Debug("连接数据库中........")
	app.Qy.DB, err = gorm.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8", app.Qy.User, app.Qy.Password, app.Qy.IP, app.Qy.Port, app.Qy.Dbname))
	if err != nil {
		cylog.Error("连接商城数据库错误：", err.Error())
		return
	}
	cylog.Debug("连接所有数据库成功")
	app.Qy.DB.DB().SetMaxIdleConns(10)
	app.Qy.DB.DB().SetMaxOpenConns(100)
	app.Qy.DB.DB().SetConnMaxLifetime(4 * time.Hour)
	app.Qy.DB.LogMode(true)
	//go syncUserToMall()
	httpInterface()

}

func httpInterface() {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(SeelogFunc())
	router.Use(gin.Recovery())
	public := router.Group("/qyapi")
	{
		public.Any("/sendUser", func(c *gin.Context) {
			txQy := app.Qy.DB.Begin()
			defer func() {
				err := recover()
				if err != nil {
					txQy.Rollback()
					cylog.Error("有错误发生，正在回滚", err)
				} else {
					txQy.Commit()
				}
			}()

			cylog.Info("==========================发送群宴用户开始:", c.Request.URL.Path, c.ClientIP())
			defer cylog.Info("==========================发送群宴用结束\n\n")

			cylog.Infof("%+v\n", c.Request)
			rtn := OutRtn{Code: 1, Message: "OK"}
			user := QyUser{}
			user.UserCode = c.PostForm("user_code")
			cylog.Info("发送用户编码:", user.UserCode)
			if err := txQy.First(&user, user.UserCode).Error; err != nil {
				cylog.Error(err)
				rtn.Code = 2
				rtn.Message = err.Error()
				c.JSON(http.StatusOK, rtn)
				return
			}
			cylog.Info("发送用户:", user.UserName)
			bytesData, err := json.Marshal(user)
			if err != nil {
				cylog.Error(user)
				cylog.Error(err)
				rtn.Code = 2
				rtn.Message = err.Error()
				c.JSON(http.StatusOK, rtn)
				return
			}
			ToURL := fmt.Sprintf("http://%s:%d/multiGo/customer/save", app.Rs.IP, app.Rs.Port)
			payload := bytes.NewReader(bytesData)
			reqest, err := http.NewRequest("POST", ToURL, payload)
			reqest.Header.Add("content-type", "application/json;charset=utf-8")
			resp, err := http.DefaultClient.Do(reqest)
			DataJSON := OutRtn{}
			if err != nil {
				cylog.Error(err)
				rtn.Code = 2
				rtn.Message = err.Error()
				c.JSON(http.StatusOK, rtn)
				return

			}
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				cylog.Error(err)
				rtn.Code = 2
				rtn.Message = err.Error()
				c.JSON(http.StatusOK, rtn)
				return

			}

			err = json.Unmarshal(body, &DataJSON)
			if err != nil {
				cylog.Error(err)
				rtn.Code = 2
				rtn.Message = err.Error()
				c.JSON(http.StatusOK, rtn)
				return

			}
			if DataJSON.Code != 1 {
				rtn.Code = DataJSON.Code
				rtn.Message = fmt.Sprintln("返回代码不正确:", DataJSON.Message)
				c.JSON(http.StatusOK, rtn)
				return
			}
			user.SyncState = 2
			if err := txQy.Model(&user).Update("sync_state", user.SyncState).Error; err != nil {
				cylog.Error(err)
			}
			cylog.Debug(rtn)
			c.JSON(http.StatusOK, rtn)
			return
		})
	}
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", app.Server.Port),
		Handler: router,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			cylog.Errorf("listen: %s\n", err)
		}
		cylog.Info("server shutdown")
	}()

	quit := make(chan os.Signal)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	cylog.Info("Shutdown Server by signal::", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		cylog.Error("Server Shutdowns:", err)
	}
	cylog.Info("Server exiting")
}

// SeelogFunc seelog 日志中间件
func SeelogFunc() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Start timer
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		if raw != "" {
			path = path + "?" + raw
		}
		cylog.Infof("请求接口 %s", path)
		// Process request
		c.Next()
		end := time.Now()
		latency := end.Sub(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()

		cylog.Infof("状态值:%d 方法:%s 路由:%s 客户端IP:%s 耗时:%13v\n", statusCode, method, path, clientIP, latency)

	}
}

// syncUserToMall 同步用户定时器
func syncUserToMall() {
	for {
		if !syncUserToMallHandler() {
			time.Sleep(120 * time.Second)
		}
	}
}

// syncUserToMallHandler 同步用户gen进程
func syncUserToMallHandler() bool {
	txQy := app.Qy.DB.Begin()
	defer func() {
		err := recover()
		if err != nil {
			txQy.Rollback()
			cylog.Error("有错误发生，正在回滚", err)
		} else {
			txQy.Commit()
		}
	}()
	userList := []QyUser{}
	// if err := txQy.Model(QyUser{}).Where("sync_state =1 and user_state=1 and user_audit_state=2 and (user_type= '乡厨' )").Where(`
	// LENGTH (user_idcard)>10
	// and user_idcard_logo_front !='images/user_idcard_logo_front.png'
	// and user_idcard_logo_front!='images/user_idcard_logo_front.png'
	// and length(user_vill)>0
	// and length(user_address)>0
	// and length(user_address)>0
	// and length(user_sex)>0
	// and user_health_logo!='images/user_health_logo.png'  and length(user_health_logo)>10
	// and  user_train_logo!='images/user_train_logo.png'  and length(user_train_logo)>10
	// and  user_registcard_logo!='images/user_registcard_logo.png'  and length(user_registcard_logo)>10
	// and user_health_datedue>now()
	// `).Limit(10).Find(&userList).Error; err != nil {
	if err := txQy.Model(QyUser{}).Where("sync_state =1 and user_state=1 and user_audit_state=2 and (user_type= '乡厨' )").Limit(10).Find(&userList).Error; err != nil {
		cylog.Error(err)
		return false
	}
	userListOther := []QyUser{}
	// if err := txQy.Model(QyUser{}).Where("sync_state =1 and user_state=1 and user_audit_state=2 and ( user_type='农家乐' or user_type='乡村酒店' ) ").Where(`
	// LENGTH (user_idcard)>10
	// and user_idcard_logo_front !='images/user_idcard_logo_front.png'
	// and user_idcard_logo_front!='images/user_idcard_logo_front.png'
	// and length(user_vill)>0
	// and length(user_address)>0
	// and length(user_address)>0
	// and length(user_sex)>0
	// and user_business_logo_url is NOT NULL and length(user_business_logo_url)>10
	// and  user_food_logo_url is NOT NULL  and length(user_food_logo_url)>10
	// and company_name is NOT NULL
	// `).Limit(10).Find(&userListOther).Error; err != nil {
	if err := txQy.Model(QyUser{}).Where("sync_state =1 and user_state=1 and user_audit_state=2 and ( user_type='农家乐' or user_type='乡村酒店' ) ").Limit(10).Find(&userListOther).Error; err != nil {
		cylog.Error(err)
		return false
	}
	for _, ele := range userListOther {
		userList = append(userList, ele)
	}
	ToURL := fmt.Sprintf("http://%s:%d/multiGo/customer/save", app.Rs.IP, app.Rs.Port)
	for index, ele := range userList {
		flag := true
		bytesData, err := json.Marshal(ele)
		//cylog.Infof("%s", bytesData)
		if err != nil {
			cylog.Error(ele)
			cylog.Error(err)
			continue
		}
		payload := bytes.NewReader(bytesData)
		//cylog.Debug("发送图片：", ToURL)
		reqest, err := http.NewRequest("POST", ToURL, payload)
		reqest.Header.Add("content-type", "application/json;charset=utf-8")
		resp, err := http.DefaultClient.Do(reqest)
		DataJSON := OutRtn{}
		if err != nil {
			cylog.Error(err)
			flag = false
			continue

		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			cylog.Error(err)
			flag = false
			goto Result

		}

		err = json.Unmarshal(body, &DataJSON)
		if err != nil {
			cylog.Error(err)
			flag = false
			goto Result

		}
		if DataJSON.Code != 1 {
			cylog.Error(DataJSON.Message)
			flag = false

		}
	Result:
		tmp := ele
		if flag { //成功
			tmp.SyncState = 2
		} else { //失败
			tmp.SyncState = 3
		}
		if err := txQy.Model(&tmp).Update("sync_state", tmp.SyncState).Error; err != nil {
			cylog.Error(err)
		}
		cylog.Info(fmt.Sprintf("第%d个同步完成", index))
	}
	cylog.Info(len(userList))
	if len(userList) == 10 || len(userList) == 20 {
		return true
	}
	return false

}

// JSONNullInt64 自定义int 结构体
type JSONNullInt64 struct {
	sql.NullInt64
}

// MarshalJSON 编译
func (v JSONNullInt64) MarshalJSON() ([]byte, error) {
	if v.Valid {
		return json.Marshal(v.Int64)
	}
	return json.Marshal(0)

}

// JSONNullFloat64 自定义int 结构体
type JSONNullFloat64 struct {
	sql.NullFloat64
}

// MarshalJSON 编译
func (v JSONNullFloat64) MarshalJSON() ([]byte, error) {
	if v.Valid {
		return json.Marshal(v.Float64)
	}
	return json.Marshal(0)

}

// JSONNullString 自定义string 结构体
type JSONNullString struct {
	sql.NullString
}

// MarshalJSON 自定义string 结构体
func (v JSONNullString) MarshalJSON() ([]byte, error) {
	if v.Valid {
		return json.Marshal(v.String)
	}
	return json.Marshal("")
}

// QyUser 群宴用户结构
type QyUser struct {
	Addtime                  JSONNullString `gorm:"column:addtime" json:"addtime,omitempty" form:"addtime"`
	BanquetCount             JSONNullInt64  `gorm:"column:banquet_count" json:"banquet_count,omitempty" form:"banquet_count"`
	CompanyName              JSONNullString `gorm:"column:company_name" json:"company_name,omitempty" form:"company_name"`
	ReportCount              JSONNullInt64  `gorm:"column:report_count" json:"report_count,omitempty" form:"report_count"`
	UserAddress              JSONNullString `gorm:"column:user_address" json:"user_address,omitempty" form:"user_address"`
	UserArea                 JSONNullString `gorm:"column:user_area" json:"user_area,omitempty" form:"user_area"`
	UserAuditState           JSONNullInt64  `gorm:"column:user_audit_state" json:"user_audit_state,omitempty" form:"user_audit_state"`
	UserBirthday             JSONNullString `gorm:"column:user_birthday" json:"user_birthday,omitempty" form:"user_birthday"`
	UserBusinessLogoURL      JSONNullString `gorm:"column:user_business_logo_url" json:"user_business_logo_url,omitempty" form:"user_business_logo_url"`
	UserCity                 JSONNullString `gorm:"column:user_city" json:"user_city,omitempty" form:"user_city"`
	UserCode                 string         `gorm:"column:user_code;primary_key" json:"user_code" form:"user_code"`
	UserEnnameShort          string         `gorm:"column:user_enname_short" json:"user_enname_short" form:"user_enname_short"`
	UserFoodLogoURL          JSONNullString `gorm:"column:user_food_logo_url" json:"user_food_logo_url,omitempty" form:"user_food_logo_url"`
	UserHealthDatedue        JSONNullString `gorm:"column:user_health_datedue" json:"user_health_datedue,omitempty" form:"user_health_datedue"`
	UserHealthLogo           JSONNullString `gorm:"column:user_health_logo" json:"user_health_logo,omitempty" form:"user_health_logo"`
	UserID                   JSONNullInt64  `gorm:"column:user_id" json:"user_id,omitempty" form:"user_id"`
	UserIdcard               JSONNullString `gorm:"column:user_idcard" json:"user_idcard,omitempty" form:"user_idcard"`
	UserIdcardExpirationtime JSONNullString `gorm:"column:user_idcard_expirationtime" json:"user_idcard_expirationtime,omitempty" form:"user_idcard_expirationtime"`
	UserIdcardGovernment     JSONNullString `gorm:"column:user_idcard_government" json:"user_idcard_government,omitempty" form:"user_idcard_government"`
	UserIdcardLogoBack       JSONNullString `gorm:"column:user_idcard_logo_back" json:"user_idcard_logo_back,omitempty" form:"user_idcard_logo_back"`
	UserIdcardLogoFront      JSONNullString `gorm:"column:user_idcard_logo_front" json:"user_idcard_logo_front,omitempty" form:"user_idcard_logo_front"`
	UserLevel                JSONNullString `gorm:"column:user_level" json:"user_level,omitempty" form:"user_level"`
	UserLoginname            JSONNullString `gorm:"column:user_loginname" json:"user_loginname,omitempty" form:"user_loginname"`
	UserLoginpass            JSONNullString `gorm:"column:user_loginpass" json:"user_loginpass,omitempty" form:"user_loginpass"`
	UserLogoURL              string         `gorm:"column:user_logo_url" json:"user_logo_url" form:"user_logo_url"`
	UserMobilephone          string         `gorm:"column:user_mobilephone" json:"user_mobilephone" form:"user_mobilephone"`
	UserName                 string         `gorm:"column:user_name" json:"user_name" form:"user_name"`
	UserNation               JSONNullString `gorm:"column:user_nation" json:"user_nation,omitempty" form:"user_nation"`
	UserProvince             JSONNullString `gorm:"column:user_province" json:"user_province,omitempty" form:"user_province"`
	UserRegistcardLogo       JSONNullString `gorm:"column:user_registcard_logo" json:"user_registcard_logo,omitempty" form:"user_registcard_logo"`
	UserRegistersource       JSONNullInt64  `gorm:"column:user_registersource" json:"user_registersource,omitempty" form:"user_registersource"`
	UserSex                  JSONNullString `gorm:"column:user_sex" json:"user_sex,omitempty" form:"user_sex"`
	UserState                int            `gorm:"column:user_state" json:"user_state" form:"user_state"`
	UserTown                 JSONNullString `gorm:"column:user_town" json:"user_town,omitempty" form:"user_town"`
	UserTrainLogo            JSONNullString `gorm:"column:user_train_logo" json:"user_train_logo,omitempty" form:"user_train_logo"`
	UserType                 string         `gorm:"column:user_type" json:"user_type" form:"user_type"`
	UserVill                 JSONNullString `gorm:"column:user_vill" json:"user_vill,omitempty" form:"user_vill"`
	SyncState                int            `gorm:"column:sync_state" json:"sync_state" form:"sync_state"`
}

// TableName sets the insert table name for this struct type
func (q *QyUser) TableName() string {
	return "qy_user"
}

// QyUserTest 群宴用户结构
type QyUserTest struct {
	Addtime                  *string       `gorm:"column:addtime" json:"addtime,omitempty"`
	CompanyName              *string       `gorm:"column:company_name" json:"company_name,omitempty"`
	UserAddress              *string       `gorm:"column:user_address" json:"user_address"`
	UserArea                 *string       `gorm:"column:user_area" json:"user_area"`
	UserAuditState           JSONNullInt64 `gorm:"column:user_audit_state" json:"user_audit_state"`
	UserBirthday             *string       `gorm:"column:user_birthday" json:"user_birthday"`
	UserBusinessLogoURL      *string       `gorm:"column:user_business_logo_url" json:"user_business_logo_url"`
	UserCity                 *string       `gorm:"column:user_city" json:"user_city"`
	UserCode                 string        `gorm:"column:user_code;primary_key" json:"user_code"`
	UserEnnameShort          string        `gorm:"column:user_enname_short" json:"user_enname_short"`
	UserFoodLogoURL          *string       `gorm:"column:user_food_logo_url" json:"user_food_logo_url"`
	UserHealthLogo           *string       `gorm:"column:user_health_logo" json:"user_health_logo"`
	UserID                   JSONNullInt64 `gorm:"column:user_id" json:"user_id"`
	UserIdcard               *string       `gorm:"column:user_idcard" json:"user_idcard"`
	UserIdcardExpirationtime *string       `gorm:"column:user_idcard_expirationtime" json:"user_idcard_expirationtime"`
	UserIdcardGovernment     *string       `gorm:"column:user_idcard_government" json:"user_idcard_government"`
	UserIdcardLogoBack       *string       `gorm:"column:user_idcard_logo_back" json:"user_idcard_logo_back"`
	UserIdcardLogoFront      *string       `gorm:"column:user_idcard_logo_front" json:"user_idcard_logo_front"`
	UserLevel                *string       `gorm:"column:user_level" json:"user_level"`
	UserLoginname            *string       `gorm:"column:user_loginname" json:"user_loginname"`
	UserLoginpass            *string       `gorm:"column:user_loginpass" json:"user_loginpass"`
	UserLogoURL              string        `gorm:"column:user_logo_url" json:"user_logo_url"`
	UserMobilephone          string        `gorm:"column:user_mobilephone" json:"user_mobilephone"`
	UserName                 string        `gorm:"column:user_name" json:"user_name"`
	UserNation               *string       `gorm:"column:user_nation" json:"user_nation"`
	UserProvince             *string       `gorm:"column:user_province" json:"user_province"`
	UserRegistcardLogo       *string       `gorm:"column:user_registcard_logo" json:"user_registcard_logo"`
	UserRegistersource       JSONNullInt64 `gorm:"column:user_registersource" json:"user_registersource"`
	UserSex                  *string       `gorm:"column:user_sex" json:"user_sex"`
	UserState                int           `gorm:"column:user_state" json:"user_state"`
	UserTown                 *string       `gorm:"column:user_town" json:"user_town"`
	UserTrainLogo            *string       `gorm:"column:user_train_logo" json:"user_train_logo"`
	UserType                 string        `gorm:"column:user_type" json:"user_type"`
	UserVill                 *string       `gorm:"column:user_vill" json:"user_vill"`
	SyncState                JSONNullInt64 `gorm:"column:sync_state" json:"sync_state"`
}

// TableName sets the insert table name for this struct type
func (q *QyUserTest) TableName() string {
	return "qy_user"
}

// OutRtn json 返回集合
type OutRtn struct {
	Code    int64       `json:"code"`
	Message string      `json:"message"`
	Total   int64       `json:"total,omitempty"`
	Offset  int64       `json:"offset,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}
